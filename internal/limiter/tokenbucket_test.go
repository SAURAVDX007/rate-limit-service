package limiter

import (
	"errors"
	"math"
	"testing"
	"time"
)

var base = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// Token counts are float64, so exact equality is unsafe (0.1+0.2 != 0.3).
func assertFloat(t *testing.T, got, want float64, label string) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("%s = %v, want %v", label, got, want)
	}
}

func TestFreshBucketAllowsExactlyCapacity(t *testing.T) {
	b := NewTokenBucket(10, 1, base)

	for i := range 10 {
		res, err := b.Allow(base, 1)
		if err != nil {
			t.Fatalf("request %d: unexpected error: %v", i, err)
		}
		if !res.Allowed {
			t.Fatalf("request %d denied, want allowed", i)
		}
	}

	res, err := b.Allow(base, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Allowed {
		t.Error("request 11 allowed, want denied")
	}
	assertFloat(t, res.RemainingTokens, 0, "remaining")
}

func TestRefillIsProportionalToElapsedTime(t *testing.T) {
	b := NewTokenBucket(10, 1, base) // 1 token/sec

	if res, _ := b.Allow(base, 10); !res.Allowed {
		t.Fatal("could not drain bucket")
	}

	// 3s later: 3 tokens accrued, 1 consumed, 2 remain.
	res, err := b.Allow(base.Add(3*time.Second), 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Allowed {
		t.Fatal("denied after refill, want allowed")
	}
	assertFloat(t, res.RemainingTokens, 2, "remaining")
}

func TestRefillClampsAtCapacity(t *testing.T) {
	b := NewTokenBucket(10, 1, base)

	if res, _ := b.Allow(base, 10); !res.Allowed {
		t.Fatal("could not drain bucket")
	}

	// An hour of idling accrues 3600 tokens in principle; capacity is 10.
	res, err := b.Allow(base.Add(time.Hour), 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertFloat(t, res.RemainingTokens, 9, "remaining")
}

func TestRetryAfterReflectsDeficitAndRate(t *testing.T) {
	b := NewTokenBucket(10, 2, base) // 2 tokens/sec

	if res, _ := b.Allow(base, 10); !res.Allowed {
		t.Fatal("could not drain bucket")
	}

	// Need 1 token, have 0, accruing 2/sec -> 500ms.
	res, err := b.Allow(base, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Allowed {
		t.Fatal("allowed on empty bucket, want denied")
	}
	if res.RetryAfter != 500*time.Millisecond {
		t.Errorf("RetryAfter = %v, want 500ms", res.RetryAfter)
	}
}

func TestRetryAfterAccountsForPartialTokens(t *testing.T) {
	b := NewTokenBucket(10, 2, base)

	if res, _ := b.Allow(base, 10); !res.Allowed {
		t.Fatal("could not drain bucket")
	}

	// 0.25s later: 0.5 tokens. Need 1, deficit 0.5, at 2/sec -> 250ms.
	res, _ := b.Allow(base.Add(250*time.Millisecond), 1)
	if res.Allowed {
		t.Fatal("allowed with 0.5 tokens against cost 1, want denied")
	}
	assertFloat(t, res.RemainingTokens, 0.5, "remaining")
	if res.RetryAfter != 250*time.Millisecond {
		t.Errorf("RetryAfter = %v, want 250ms", res.RetryAfter)
	}
}

func TestFractionalCostsAccumulateExactly(t *testing.T) {
	b := NewTokenBucket(10, 1, base)

	for range 3 {
		if res, err := b.Allow(base, 0.3); err != nil || !res.Allowed {
			t.Fatalf("fractional request failed: allowed=%v err=%v", res.Allowed, err)
		}
	}

	res, _ := b.Allow(base, 0)
	assertFloat(t, res.RemainingTokens, 9.1, "remaining after 3x0.3")
}

func TestCostAboveCapacityIsAnError(t *testing.T) {
	b := NewTokenBucket(10, 1, base)

	_, err := b.Allow(base, 11)
	if !errors.Is(err, ErrCostExceedsCapacity) {
		t.Errorf("err = %v, want ErrCostExceedsCapacity", err)
	}
}

func TestClockGoingBackwardsDoesNotDrainTokens(t *testing.T) {
	b := NewTokenBucket(10, 1, base)

	if res, _ := b.Allow(base, 5); !res.Allowed {
		t.Fatal("setup request denied")
	}

	// Negative elapsed must clamp to zero, not subtract tokens.
	res, err := b.Allow(base.Add(-10*time.Second), 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Allowed {
		t.Fatal("denied, want allowed")
	}
	assertFloat(t, res.RemainingTokens, 4, "remaining")
}

func TestResetAtIsWhenBucketWouldBeFull(t *testing.T) {
	b := NewTokenBucket(10, 2, base) // 2 tokens/sec

	// Consume 4 -> 6 remain -> 4 short -> 2s to full.
	res, _ := b.Allow(base, 4)
	want := base.Add(2 * time.Second)
	if !res.ResetAt.Equal(want) {
		t.Errorf("ResetAt = %v, want %v", res.ResetAt, want)
	}
}

func TestResetAtIsSetOnDeniedRequests(t *testing.T) {
	b := NewTokenBucket(10, 2, base)

	if res, _ := b.Allow(base, 10); !res.Allowed {
		t.Fatal("could not drain bucket")
	}

	res, _ := b.Allow(base, 1)
	if res.Allowed {
		t.Fatal("allowed on empty bucket, want denied")
	}
	if res.ResetAt.IsZero() {
		t.Error("ResetAt is zero on a denied request; callers need " +
			"X-RateLimit-Reset precisely on 429 responses")
	}
}
