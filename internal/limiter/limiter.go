package limiter

import (
	"context"
	"errors"
	"time"

	"github.com/sauravdx007/rate-limit-service/internal/rules"
)

var ErrCostExceedsCapacity = errors.New("cost exceeds bucket capacity")

// Decision is the outcome of evaluating one request against one rule.
type Decision struct {
	Allowed   bool    `json:"allowed"`
	Limit     float64 `json:"limit"`     // the ceiling this rule enforces
	Remaining float64 `json:"remaining"` // capacity left after this decision

	RetryAfter time.Duration `json:"-"`
	ResetAt    time.Time     `json:"-"`
}

type Limiter interface {
	Allow(ctx context.Context, key string, rule rules.Rule, cost float64) (Decision, error)
	Peek(ctx context.Context, key string, rule rules.Rule, cost float64) (Decision, error)
}
