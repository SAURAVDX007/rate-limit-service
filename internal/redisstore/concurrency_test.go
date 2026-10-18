package redisstore

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/redis/go-redis/v9"

	"github.com/sauravdx007/rate-limit-service/internal/rules"
)

func testClient(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		t.Skipf("redis not available at %s: %v", addr, err)
	}
	return rdb
}

// TestConcurrentEvaluationsNeverExceedCapacity is the core correctness test.
//
// 500 goroutines evaluate the same key simultaneously against a capacity of
// 100 with no refill. Exactly 100 must be allowed.
//
// This fails against the obvious wrong implementation — GET, decide in Go,
// SET — because many goroutines read the same count before any of them
// writes, and all of them conclude there is room. It passes here because the
// whole decision runs inside one Lua script, which Redis executes to
// completion without interleaving.
//
// Run with -race. The race detector catches unsynchronised access inside this
// process; it cannot see the cross-process race, which is precisely why the
// atomicity has to live in Redis rather than in a mutex.
func TestConcurrentEvaluationsNeverExceedCapacity(t *testing.T) {
	rdb := testClient(t)
	defer rdb.Close()

	ctx := context.Background()
	key := "test:concurrent:" + t.Name()
	rdb.Del(ctx, key)
	defer rdb.Del(ctx, key)

	const (
		capacity   = 100
		goroutines = 500
	)

	rule := rules.Rule{
		Identifier: "test",
		Namespace:  "concurrent",
		Algorithm:  rules.TokenBucket,
		Capacity:   capacity,
		// Refill slow enough that nothing accrues during the test.
		RefillRate: 0.0001,
	}

	store := New(rdb)

	var allowed, denied, failed int64
	var wg sync.WaitGroup
	start := make(chan struct{}) // release all goroutines at once

	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			dec, err := store.Allow(ctx, key, rule, 1)
			switch {
			case err != nil:
				atomic.AddInt64(&failed, 1)
			case dec.Allowed:
				atomic.AddInt64(&allowed, 1)
			default:
				atomic.AddInt64(&denied, 1)
			}
		}()
	}

	close(start)
	wg.Wait()

	if failed != 0 {
		t.Fatalf("%d evaluations errored", failed)
	}
	if allowed != capacity {
		t.Errorf("allowed = %d, want exactly %d (over-admission means the "+
			"read-modify-write is not atomic)", allowed, capacity)
	}
	if allowed+denied != goroutines {
		t.Errorf("allowed+denied = %d, want %d", allowed+denied, goroutines)
	}
}

// TestPeekDoesNotConsume proves the observe path is side-effect free.
func TestPeekDoesNotConsume(t *testing.T) {
	rdb := testClient(t)
	defer rdb.Close()

	ctx := context.Background()
	key := "test:peek:" + t.Name()
	rdb.Del(ctx, key)
	defer rdb.Del(ctx, key)

	rule := rules.Rule{
		Identifier: "test", Namespace: "peek",
		Algorithm: rules.TokenBucket, Capacity: 10, RefillRate: 0.0001,
	}
	store := New(rdb)

	for range 20 {
		dec, err := store.Peek(ctx, key, rule, 1)
		if err != nil {
			t.Fatalf("peek: %v", err)
		}
		if dec.Remaining != 10 {
			t.Fatalf("remaining = %v after peek, want 10 — peek consumed capacity", dec.Remaining)
		}
	}
}

// TestSeparateKeysDoNotShareCapacity guards against key construction bugs
// that would let one tenant's traffic exhaust another's quota.
func TestSeparateKeysDoNotShareCapacity(t *testing.T) {
	rdb := testClient(t)
	defer rdb.Close()

	ctx := context.Background()
	rule := rules.Rule{
		Identifier: "test", Namespace: "isolation",
		Algorithm: rules.TokenBucket, Capacity: 2, RefillRate: 0.0001,
	}
	store := New(rdb)

	for _, key := range []string{"test:iso:a", "test:iso:b"} {
		rdb.Del(ctx, key)
		defer rdb.Del(ctx, key)

		for i := range 2 {
			dec, _ := store.Allow(ctx, key, rule, 1)
			if !dec.Allowed {
				t.Fatalf("%s request %d denied, want allowed", key, i)
			}
		}
		dec, _ := store.Allow(ctx, key, rule, 1)
		if dec.Allowed {
			t.Fatalf("%s third request allowed, want denied", key)
		}
	}
}
