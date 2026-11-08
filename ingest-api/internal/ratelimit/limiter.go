package ratelimit

import (
	"sync"
	"time"
)

type bucket struct {
	tokens     float64
	refilledAt time.Time
}

type Limiter struct {
	mutex           sync.Mutex
	now             func() time.Time
	capacity        float64
	refillPerSecond float64
	buckets         map[string]*bucket
}

func New(recordsPerMinute int64, now func() time.Time) *Limiter {
	return &Limiter{
		now:             now,
		capacity:        float64(recordsPerMinute),
		refillPerSecond: float64(recordsPerMinute) / 60,
		buckets:         map[string]*bucket{},
	}
}

func (limiter *Limiter) HasToken(keyID string) (bool, time.Duration) {
	return limiter.take(keyID, 1, false)
}

func (limiter *Limiter) Take(keyID string, count int) (bool, time.Duration) {
	return limiter.take(keyID, float64(count), true)
}

func (limiter *Limiter) take(keyID string, needed float64, consume bool) (bool, time.Duration) {
	limiter.mutex.Lock()
	defer limiter.mutex.Unlock()

	currentTime := limiter.now()
	keyBucket, exists := limiter.buckets[keyID]
	if !exists {
		keyBucket = &bucket{tokens: limiter.capacity, refilledAt: currentTime}
		limiter.buckets[keyID] = keyBucket
	}
	elapsedSeconds := max(0, currentTime.Sub(keyBucket.refilledAt).Seconds())
	keyBucket.tokens = min(limiter.capacity, keyBucket.tokens+elapsedSeconds*limiter.refillPerSecond)
	keyBucket.refilledAt = currentTime

	if keyBucket.tokens < needed {
		missingSeconds := (needed - keyBucket.tokens) / limiter.refillPerSecond
		return false, time.Duration(missingSeconds * float64(time.Second))
	}
	if consume {
		keyBucket.tokens -= needed
	}
	return true, 0
}
