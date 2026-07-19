package policy

import (
	"sync"
	"time"

	"beacon/internal/config"
)

// RateLimiter enforces per-(service, channel) throughput caps. Implementations
// return whether the request may proceed and, when blocked, how long to wait.
type RateLimiter interface {
	Allow(service, channel string, rc config.RateConfig) (ok bool, retryAfter time.Duration)
}

// MemoryLimiter is a token-bucket (rpm) + daily-counter limiter. In-memory:
// suitable for the single-instance deployment; counters reset on restart.
type MemoryLimiter struct {
	mu      sync.Mutex
	now     func() time.Time
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
	day    string
	daily  int
}

// NewMemoryLimiter creates a limiter; pass nil for wall-clock time.
func NewMemoryLimiter(now func() time.Time) *MemoryLimiter {
	if now == nil {
		now = time.Now
	}
	return &MemoryLimiter{now: now, buckets: make(map[string]*bucket)}
}

func (l *MemoryLimiter) Allow(service, channel string, rc config.RateConfig) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	key := service + "|" + channel
	now := l.now()
	today := now.UTC().Format("2006-01-02")

	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: float64(rc.RPM), last: now, day: today}
		l.buckets[key] = b
	}

	perSecond := float64(rc.RPM) / 60.0
	b.tokens = min(float64(rc.RPM), b.tokens+now.Sub(b.last).Seconds()*perSecond)
	b.last = now

	if b.day != today {
		b.day = today
		b.daily = 0
	}
	if rc.Daily > 0 && b.daily >= rc.Daily {
		midnight := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC).Add(24 * time.Hour)
		return false, midnight.Sub(now.UTC())
	}
	if b.tokens < 1 {
		wait := time.Duration((1 - b.tokens) / perSecond * float64(time.Second))
		return false, wait
	}
	b.tokens--
	b.daily++
	return true, 0
}
