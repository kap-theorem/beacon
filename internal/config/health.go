package config

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// ReadinessCheck is one named readiness condition.
type ReadinessCheck struct {
	Name string
	Fn   func(ctx context.Context) error
}

// HealthChecker evaluates readiness checks with a short result cache so
// probes don't hammer dependencies.
type HealthChecker struct {
	mu        sync.Mutex
	checks    []ReadinessCheck
	ttl       time.Duration
	lastErr   error
	checkedAt time.Time
}

// NewHealthChecker constructs a HealthChecker from the given readiness
// checks. A checker constructed with zero checks always reports ready —
// callers must register real checks in production wiring.
func NewHealthChecker(checks ...ReadinessCheck) *HealthChecker {
	return &HealthChecker{checks: checks, ttl: 5 * time.Second}
}

// readiness evaluates (or returns the cached result of) all registered
// checks. The evaluation is shared, cached state consumed by all probers;
// it must not inherit any single caller's context or one short-timeout
// prober would cache its own cancellation as a fleet-wide failure.
func (hc *HealthChecker) readiness() error {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	if !hc.checkedAt.IsZero() && time.Since(hc.checkedAt) < hc.ttl {
		return hc.lastErr
	}
	cctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var err error
	for _, c := range hc.checks {
		if e := c.Fn(cctx); e != nil {
			err = fmt.Errorf("%s: %w", c.Name, e)
			break
		}
	}
	hc.checkedAt = time.Now()
	hc.lastErr = err
	return err
}

func (hc *HealthChecker) HandleLive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

func (hc *HealthChecker) HandleReady(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := hc.readiness(); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, "not ready: %v", err)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ready"))
}
