package policy

import (
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
