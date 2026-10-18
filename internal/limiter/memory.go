package limiter

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/sauravdx007/rate-limit-service/internal/rules"
)

type Memory struct {
	mu      sync.Mutex
	buckets map[string]*TokenBucket
	now     func() time.Time
}

func NewMemory(now func() time.Time) *Memory {
	if now == nil {
		now = time.Now
	}
	return &Memory{
		buckets: make(map[string]*TokenBucket),
		now:     now,
	}
}

func (m *Memory) Allow(ctx context.Context, key string, rule rules.Rule, cost float64) (Decision, error) {
	return m.evaluate(key, rule, cost, true)
}

func (m *Memory) Peek(ctx context.Context, key string, rule rules.Rule, cost float64) (Decision, error) {
	return m.evaluate(key, rule, cost, false)
}

func (m *Memory) evaluate(key string, rule rules.Rule, cost float64, consume bool) (Decision, error) {
	if err := rule.Validate(); err != nil {
		return Decision{}, fmt.Errorf("invalid rule: %w", err)
	}

	switch rule.Algorithm {
	case rules.TokenBucket:
		return m.tokenBucket(key, rule, cost, consume)
	default:
		return Decision{}, fmt.Errorf("algorithm %q not implemented in memory limiter", rule.Algorithm)
	}
}

func (m *Memory) tokenBucket(key string, rule rules.Rule, cost float64, consume bool) (Decision, error) {
	now := m.now()

	m.mu.Lock()
	defer m.mu.Unlock()

	b, ok := m.buckets[key]
	if !ok {
		// A missing bucket is indistinguishable from a full one — the same
		// equivalence that makes TTL eviction safe in the Redis implementation.
		b = NewTokenBucket(rule.Capacity, rule.RefillRate, now)
		m.buckets[key] = b
	}

	var (
		res Result
		err error
	)
	if consume {
		res, err = b.Allow(now, cost)
	} else {
		res, err = b.Peek(now, cost)
	}
	if err != nil {
		return Decision{}, err
	}

	return Decision{
		Allowed:    res.Allowed,
		Limit:      rule.Capacity,
		Remaining:  res.RemainingTokens,
		RetryAfter: res.RetryAfter,
		ResetAt:    res.ResetAt,
	}, nil
}
