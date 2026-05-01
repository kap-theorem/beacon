package config

import (
	"net/http"
	"sync"
	"sync/atomic"
)

type HealthChecker struct {
	ready    atomic.Bool
	mu       sync.RWMutex
	lastErr  error
	cacheAge func() (bool, string)
}

func NewHealthChecker() *HealthChecker {
	return &HealthChecker{}
}

func (hc *HealthChecker) SetReady(ready bool) {
	hc.ready.Store(ready)
}

func (hc *HealthChecker) SetError(err error) {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	hc.lastErr = err
}

func (hc *HealthChecker) HandleLive(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

func (hc *HealthChecker) HandleReady(w http.ResponseWriter, r *http.Request) {
	if !hc.ready.Load() {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("not ready"))
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ready"))
}
