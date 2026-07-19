package policy

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"beacon/internal/config"
)

func TestMemoryLimiter_RPMExhaustionAndRefill(t *testing.T) {
	now := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	l := NewMemoryLimiter(func() time.Time { return now })
	rc := config.RateConfig{RPM: 2, Daily: 100}

	for i := 0; i < 2; i++ {
		if ok, _ := l.Allow("svc", "email", rc); !ok {
			t.Fatalf("request %d should pass", i+1)
		}
	}
	ok, retryAfter := l.Allow("svc", "email", rc)
	if ok {
		t.Fatal("third request in the same instant must be limited")
	}
	if retryAfter <= 0 {
		t.Fatalf("want positive retry-after, got %v", retryAfter)
	}

	now = now.Add(31 * time.Second) // one token refilled at 2/min
	if ok, _ := l.Allow("svc", "email", rc); !ok {
		t.Fatal("request after refill should pass")
	}
}

func TestMemoryLimiter_DailyQuotaResetsAtUTCMidnight(t *testing.T) {
	now := time.Date(2026, 7, 18, 23, 59, 0, 0, time.UTC)
	l := NewMemoryLimiter(func() time.Time { return now })
	rc := config.RateConfig{RPM: 1000, Daily: 1}

	if ok, _ := l.Allow("svc", "email", rc); !ok {
		t.Fatal("first daily request should pass")
	}
	ok, retryAfter := l.Allow("svc", "email", rc)
	if ok {
		t.Fatal("daily quota of 1 must block the second request")
	}
	if retryAfter < 30*time.Second || retryAfter > time.Minute {
		t.Fatalf("retry-after should point at UTC midnight (~60s away), got %v", retryAfter)
	}

	now = now.Add(2 * time.Minute) // past midnight UTC
	if ok, _ := l.Allow("svc", "email", rc); !ok {
		t.Fatal("quota must reset after UTC midnight")
	}
}

func TestMemoryLimiter_IsolatedPerServiceChannel(t *testing.T) {
	now := time.Now()
	l := NewMemoryLimiter(func() time.Time { return now })
	rc := config.RateConfig{RPM: 1, Daily: 100}
	l.Allow("svc-a", "email", rc)
	if ok, _ := l.Allow("svc-b", "email", rc); !ok {
		t.Fatal("svc-b must have its own bucket")
	}
}

// TestMemoryLimiter_ConcurrentContentionExactQuota hammers a single
// (service, channel) bucket from many goroutines simultaneously to verify
// the mutex correctly serializes token accounting: no more than RPM requests
// may succeed no matter how much concurrent contention there is. The clock is
// frozen (never advances) so no mid-test refill can mask an accounting bug -
// only mutual exclusion determines the outcome. Run with -race to confirm
// there's no data race on the shared bucket state.
func TestMemoryLimiter_ConcurrentContentionExactQuota(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC) // frozen: never advances
	l := NewMemoryLimiter(func() time.Time { return now })
	rc := config.RateConfig{RPM: 10, Daily: 1000}

	const goroutines = 8
	const attemptsPerGoroutine = 25 // 8*25=200 attempts, far exceeding the RPM=10 cap

	var wg sync.WaitGroup
	var allowed int64

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < attemptsPerGoroutine; j++ {
				if ok, _ := l.Allow("svc", "email", rc); ok {
					atomic.AddInt64(&allowed, 1)
				}
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt64(&allowed); got != 10 {
		t.Fatalf("want exactly 10 allowed under concurrent contention (RPM=10, frozen clock), got %d", got)
	}
}

// TestMemoryLimiter_DayRolloverCombinedWithRefill exercises the interaction
// between token refill and the UTC-day daily-quota reset, both triggered by
// the same clock advance across midnight.
func TestMemoryLimiter_DayRolloverCombinedWithRefill(t *testing.T) {
	now := time.Date(2026, 7, 18, 23, 59, 0, 0, time.UTC)
	l := NewMemoryLimiter(func() time.Time { return now })
	rc := config.RateConfig{RPM: 2, Daily: 5}

	// Drain both RPM tokens on 2026-07-18. This also consumes 2 of the
	// daily quota of 5 - usage that must NOT carry over into 07-19.
	for i := 0; i < 2; i++ {
		if ok, _ := l.Allow("svc", "email", rc); !ok {
			t.Fatalf("token %d should pass before exhaustion", i+1)
		}
	}
	if ok, _ := l.Allow("svc", "email", rc); ok {
		t.Fatal("third request in the same instant must be limited (tokens exhausted)")
	}

	// Advance 90s, crossing UTC midnight into 2026-07-19T00:00:30Z.
	// Token refill: 90s at RPM=2 (1 token/30s) => 3 tokens generated,
	// clamped to the bucket's RPM=2 capacity. This call also crosses the
	// UTC day boundary, so the daily counter must reset to 0 here too.
	now = now.Add(90 * time.Second)
	if ok, _ := l.Allow("svc", "email", rc); !ok {
		t.Fatal("request after crossing midnight should pass: tokens refilled and daily counter reset")
	}

	// Continue consuming through the new day, spaced 31s apart so each call
	// has at least one fresh token (RPM=2 => 1 token/30s). This is a
	// regression check on the daily-counter reset: yesterday's usage was 2
	// of the daily quota of 5. The Allow() check blocks when
	// `daily >= rc.Daily` (i.e. >= 5). If the daily counter incorrectly
	// carried over from yesterday (starting at 2 instead of resetting to 0),
	// the running count after the midnight-crossing call above would be 3,
	// and after i=0,1 below would be 4, then 5 - so the call at i=2 (the 4th
	// post-midnight call overall) would see daily==5 and be blocked. Since
	// the counter correctly resets at midnight, the post-midnight count
	// starts at 0 and all 5 of today's calls succeed.
	for i := 0; i < 4; i++ {
		now = now.Add(31 * time.Second)
		if ok, _ := l.Allow("svc", "email", rc); !ok {
			t.Fatalf("post-midnight allow %d should pass (daily counter must have reset)", i+2)
		}
	}
}
