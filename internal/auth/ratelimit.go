package auth

import (
	"sync"
	"time"

	"ai-memory/internal/apperror"

	"github.com/google/uuid"
)

type bucket struct {
	tokens     float64
	lastRefill time.Time
}

// RateLimiter enforces per-principal write rate limits.
type RateLimiter struct {
	mu            sync.Mutex
	ratePerSecond float64
	burst         float64
	buckets       map[uuid.UUID]*bucket
}

func NewRateLimiter(ratePerMinute, burst int) *RateLimiter {
	if ratePerMinute <= 0 {
		ratePerMinute = 120
	}
	if burst <= 0 {
		burst = 30
	}
	return &RateLimiter{
		ratePerSecond: float64(ratePerMinute) / 60.0,
		burst:         float64(burst),
		buckets:       make(map[uuid.UUID]*bucket),
	}
}

// Allow checks whether principalID has available write quota.
func (rl *RateLimiter) Allow(principalID uuid.UUID) error {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	b, ok := rl.buckets[principalID]
	if !ok {
		rl.buckets[principalID] = &bucket{
			tokens:     rl.burst - 1,
			lastRefill: now,
		}
		return nil
	}

	elapsed := now.Sub(b.lastRefill).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * rl.ratePerSecond
		if b.tokens > rl.burst {
			b.tokens = rl.burst
		}
		b.lastRefill = now
	}

	if b.tokens < 1.0 {
		return apperror.New(apperror.CodeRateLimited, "write rate limit exceeded for principal")
	}
	b.tokens -= 1.0
	return nil
}
