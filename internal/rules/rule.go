package rules

import (
	"errors"
	"fmt"
)

type Algorithm string

const (
	TokenBucket   Algorithm = "token_bucket"
	SlidingWindow Algorithm = "sliding_window"
	FixedWindow   Algorithm = "fixed_window"
)

type Rule struct {
	Identifier string    `json:"identifier"`
	Namespace  string    `json:"namespace"`
	Algorithm  Algorithm `json:"algorithm"`

	// token bucket
	Capacity   float64 `json:"capacity,omitempty"`
	RefillRate float64 `json:"refill_rate,omitempty"`

	// sliding_window, fixed_window
	Limit         float64 `json:"limit,omitempty"`
	WindowSeconds int64   `json:"window_seconds,omitempty"`
}

func (r Rule) Validate() error {
	if r.Identifier == "" {
		return errors.New("identifier is required (use \"*\" to match any)")
	}
	if r.Namespace == "" {
		return errors.New("namespace is required (use \"*\" to match any)")
	}

	switch r.Algorithm {
	case TokenBucket:
		if r.Capacity <= 0 {
			return errors.New("token_bucket: capacity must be positive")
		}
		if r.RefillRate <= 0 {
			return errors.New("token_bucket: refill_rate must be positive")
		}
		if r.Limit != 0 || r.WindowSeconds != 0 {
			return errors.New("token_bucket: limit and window_seconds do not apply")
		}

	case SlidingWindow, FixedWindow:
		if r.Limit <= 0 {
			return fmt.Errorf("%s: limit must be positive", r.Algorithm)
		}
		if r.WindowSeconds <= 0 {
			return fmt.Errorf("%s: window_seconds must be positive", r.Algorithm)
		}
		if r.Capacity != 0 || r.RefillRate != 0 {
			return fmt.Errorf("%s: capacity and refill_rate do not apply", r.Algorithm)
		}

	default:
		return fmt.Errorf("unknown algorithm %q (want token_bucket, sliding_window, or fixed_window)", r.Algorithm)
	}

	return nil
}
