package ratelimit

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeClock struct {
	current time.Time
}

func (clock *fakeClock) now() time.Time {
	return clock.current
}

func (clock *fakeClock) advance(duration time.Duration) {
	clock.current = clock.current.Add(duration)
}

func newTestLimiter(recordsPerMinute int64) (*Limiter, *fakeClock) {
	clock := &fakeClock{current: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	return New(recordsPerMinute, clock.now), clock
}

func TestNewBucketAllowsFullCapacityThenDenies(t *testing.T) {
	limiter, _ := newTestLimiter(600)

	if allowed, _ := limiter.Take("key", 600); !allowed {
		t.Fatal("full capacity take was denied")
	}
	if allowed, _ := limiter.HasToken("key"); allowed {
		t.Fatal("HasToken allowed on an empty bucket")
	}
}

func TestTakeOverRemainingTokensConsumesNothing(t *testing.T) {
	limiter, _ := newTestLimiter(600)
	limiter.Take("key", 500)

	if allowed, _ := limiter.Take("key", 101); allowed {
		t.Fatal("take above remaining tokens was allowed")
	}
	if allowed, _ := limiter.Take("key", 100); !allowed {
		t.Fatal("take of the remaining tokens was denied after a failed take")
	}
}

func TestRefillIsContinuousAndCappedAtCapacity(t *testing.T) {
	limiter, clock := newTestLimiter(600000)
	limiter.Take("key", 600000)

	clock.advance(30 * time.Second)
	if allowed, _ := limiter.Take("key", 300001); allowed {
		t.Fatal("more than the refilled tokens were available")
	}
	if allowed, _ := limiter.Take("key", 300000); !allowed {
		t.Fatal("refilled tokens were not available")
	}

	clock.advance(10 * time.Minute)
	if allowed, _ := limiter.Take("key", 600001); allowed {
		t.Fatal("refill exceeded capacity")
	}
	if allowed, _ := limiter.Take("key", 600000); !allowed {
		t.Fatal("full capacity was not restored")
	}
}

func TestRetryAfterReflectsMissingTokens(t *testing.T) {
	limiter, _ := newTestLimiter(60)
	limiter.Take("key", 60)

	if _, retryAfter := limiter.HasToken("key"); retryAfter != time.Second {
		t.Fatalf("HasToken retryAfter %v, want 1s", retryAfter)
	}
	if _, retryAfter := limiter.Take("key", 30); retryAfter != 30*time.Second {
		t.Fatalf("Take retryAfter %v, want 30s", retryAfter)
	}
}

func TestKeysAreIndependent(t *testing.T) {
	limiter, _ := newTestLimiter(10)
	limiter.Take("first", 10)

	if allowed, _ := limiter.HasToken("first"); allowed {
		t.Fatal("first key should be empty")
	}
	if allowed, _ := limiter.Take("second", 10); !allowed {
		t.Fatal("second key was affected by the first")
	}
}

func TestConcurrentTakesNeverExceedCapacity(t *testing.T) {
	const capacity = 1000
	limiter, _ := newTestLimiter(capacity)
	var succeeded atomic.Int64
	var waitGroup sync.WaitGroup
	for goroutine := 0; goroutine < 5000; goroutine++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			if allowed, _ := limiter.Take("key", 1); allowed {
				succeeded.Add(1)
			}
		}()
	}
	waitGroup.Wait()

	if succeeded.Load() != capacity {
		t.Fatalf("%d takes succeeded, want %d", succeeded.Load(), capacity)
	}
}
