package limiter

import (
	"time"
)

type TokenBucket struct {
	tokens     float64
	lastRefill time.Time
	capacity   float64
	refillRate float64
}

type Result struct {
	Allowed         bool
	RemainingTokens float64
	RetryAfter      time.Duration
	ResetAt         time.Time
}

func NewTokenBucket(capacity, refillRate float64, now time.Time) *TokenBucket {
	return &TokenBucket{
		tokens:     capacity, // initially give capacity amount of tokens
		lastRefill: now,
		capacity:   capacity,
		refillRate: refillRate,
	}
}

func (b *TokenBucket) Allow(now time.Time, cost float64) (Result, error) {
	if cost > b.capacity {
		return Result{}, ErrCostExceedsCapacity
	}
	elapsed := now.Sub(b.lastRefill)
	if elapsed < 0 {
		elapsed = 0
	}
	b.tokens = min(b.capacity, b.tokens+elapsed.Seconds()*b.refillRate)
	b.lastRefill = now

	res := Result{}
	if b.tokens >= cost {
		b.tokens -= cost
		res.Allowed = true
		res.RemainingTokens = b.tokens
		res.ResetAt = now.Add(b.timeToFull())
		return res, nil
	}
	res.RemainingTokens = b.tokens
	res.ResetAt = now.Add(b.timeToFull())
	if b.refillRate > 0 {
		res.RetryAfter = time.Duration((cost - b.tokens) / b.refillRate * float64(time.Second))
	}
	return res, nil
}

func (b *TokenBucket) timeToFull() time.Duration {
	if b.refillRate <= 0 {
		return 0
	}
	return time.Duration((b.capacity - b.tokens) / b.refillRate * float64(time.Second))
}

// Peek reports what Allow would decide, without consuming tokens or
// mutating the bucket. Refill is computed on a copy.
func (b *TokenBucket) Peek(now time.Time, cost float64) (Result, error) {
	snapshot := *b
	res, err := snapshot.Allow(now, cost)
	if !res.Allowed {
		return res, err
	}
	// Allow deducted from the copy; report pre-deduction capacity.
	res.RemainingTokens += cost
	return res, err
}
