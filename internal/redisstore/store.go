// Package redisstore implements limiter.Limiter against Redis.
//
// The entire decision — read state, refill, compare, write back — executes
// inside a single Lua script. Redis is single-threaded, so each *command* is
// atomic, but a GET-decide-SET sequence from application code is not: two
// instances can read the same value before either writes, and both allow.
// Running the whole decision server-side closes that window, and does it in
// one round trip with no retry loop.
package redisstore

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/sauravdx007/rate-limit-service/internal/limiter"
	"github.com/sauravdx007/rate-limit-service/internal/rules"
)

// tokenBucketScript evaluates a token bucket atomically.
//
// KEYS[1] bucket key
// ARGV[1] capacity        ARGV[2] refill rate (tokens/sec)
// ARGV[3] cost            ARGV[4] 1 to consume, 0 to peek
//
// Returns: { allowed, remaining (milli-tokens), retry_after_ms, reset_ms }
//
// Time comes from redis.call('TIME'), never from the caller. Each instance
// has its own system clock, and NTP drift of tens of milliseconds is normal.
// Since refill is (now - last_refill) * rate, mixing clocks makes elapsed
// time wrong and can make it negative. One server clock, read inside the
// critical section, removes the problem entirely.
//
// Floats are passed as strings and returned as milli-units because Lua->Redis
// return values are integers; fractional tokens would otherwise be truncated.
const tokenBucketScript = `
local key        = KEYS[1]
local capacity   = tonumber(ARGV[1])
local rate       = tonumber(ARGV[2])
local cost       = tonumber(ARGV[3])
local consume    = tonumber(ARGV[4]) == 1

local t   = redis.call('TIME')
local now = tonumber(t[1]) + tonumber(t[2]) / 1000000

local state       = redis.call('HMGET', key, 'tokens', 'ts')
local tokens      = tonumber(state[1])
local last_refill = tonumber(state[2])

-- A missing key is indistinguishable from a full bucket. That equivalence is
-- what makes TTL eviction of idle keys safe.
if tokens == nil then
  tokens      = capacity
  last_refill = now
end

local elapsed = now - last_refill
if elapsed < 0 then elapsed = 0 end

tokens = math.min(capacity, tokens + elapsed * rate)

local allowed = tokens >= cost
if allowed and consume then
  tokens = tokens - cost
end

if consume then
  redis.call('HSET', key, 'tokens', tokens, 'ts', now)
  -- Expire once the bucket would be full again, with a small margin. At that
  -- point the stored state and a fresh full bucket are equivalent, so the key
  -- carries no information worth keeping.
  local ttl = math.ceil((capacity - tokens) / rate) + 10
  redis.call('EXPIRE', key, ttl)
end

local retry_after_ms = 0
if not allowed then
  retry_after_ms = math.ceil(((cost - tokens) / rate) * 1000)
end
local reset_ms = math.ceil(((capacity - tokens) / rate) * 1000)

return {
  allowed and 1 or 0,
  math.floor(tokens * 1000),
  retry_after_ms,
  reset_ms,
}
`

type Store struct {
	rdb    *redis.Client
	bucket *redis.Script
}

func New(rdb *redis.Client) *Store {
	return &Store{
		rdb:    rdb,
		bucket: redis.NewScript(tokenBucketScript),
	}
}

func (s *Store) Allow(ctx context.Context, key string, rule rules.Rule, cost float64) (limiter.Decision, error) {
	return s.eval(ctx, key, rule, cost, true)
}

func (s *Store) Peek(ctx context.Context, key string, rule rules.Rule, cost float64) (limiter.Decision, error) {
	return s.eval(ctx, key, rule, cost, false)
}

func (s *Store) eval(ctx context.Context, key string, rule rules.Rule, cost float64, consume bool) (limiter.Decision, error) {
	if err := rule.Validate(); err != nil {
		return limiter.Decision{}, fmt.Errorf("invalid rule: %w", err)
	}
	if rule.Algorithm != rules.TokenBucket {
		return limiter.Decision{}, fmt.Errorf("algorithm %q not implemented", rule.Algorithm)
	}
	if cost > rule.Capacity {
		return limiter.Decision{}, limiter.ErrCostExceedsCapacity
	}

	consumeFlag := 0
	if consume {
		consumeFlag = 1
	}

	// Run uses EVALSHA and falls back to EVAL if the script isn't cached.
	raw, err := s.bucket.Run(ctx, s.rdb, []string{key},
		rule.Capacity, rule.RefillRate, cost, consumeFlag).Slice()
	if err != nil {
		return limiter.Decision{}, fmt.Errorf("redis eval: %w", err)
	}
	if len(raw) != 4 {
		return limiter.Decision{}, fmt.Errorf("script returned %d values, want 4", len(raw))
	}

	ints := make([]int64, 4)
	for i, v := range raw {
		n, ok := v.(int64)
		if !ok {
			return limiter.Decision{}, fmt.Errorf("script value %d is %T, want int64", i, v)
		}
		ints[i] = n
	}

	return limiter.Decision{
		Allowed:    ints[0] == 1,
		Limit:      rule.Capacity,
		Remaining:  float64(ints[1]) / 1000,
		RetryAfter: time.Duration(ints[2]) * time.Millisecond,
		ResetAt:    time.Now().Add(time.Duration(ints[3]) * time.Millisecond),
	}, nil
}
