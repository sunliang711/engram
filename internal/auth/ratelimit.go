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

// RateLimiter enforces token-bucket rate limits and periodically evicts idle buckets.
type RateLimiter struct {
	mu            sync.Mutex
	ratePerSecond float64
	burst         float64
	buckets       map[string]*bucket
	lastCleanup   time.Time
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
		buckets:       make(map[string]*bucket),
		lastCleanup:   time.Now(),
	}
}

// Allow checks whether principalID has available write quota.
func (rl *RateLimiter) Allow(principalID uuid.UUID) error {
	return rl.AllowKey(principalID.String())
}

// AllowKey checks whether an arbitrary key (e.g. principal UUID or client IP) has available quota.
func (rl *RateLimiter) AllowKey(key string) error {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	if now.Sub(rl.lastCleanup) > 5*time.Minute {
		for k, b := range rl.buckets {
			if now.Sub(b.lastRefill) > 15*time.Minute {
				delete(rl.buckets, k)
			}
		}
		rl.lastCleanup = now
	}

	b, ok := rl.buckets[key]
	if !ok {
		rl.buckets[key] = &bucket{
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
		return apperror.New(apperror.CodeRateLimited, "rate limit exceeded")
	}
	b.tokens -= 1.0
	return nil
}
