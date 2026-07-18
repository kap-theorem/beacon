package config

import (
	"net/http"
	"sync/atomic"
)

type HealthChecker struct {
	ready atomic.Bool
}

func NewHealthChecker() *HealthChecker {
	return &HealthChecker{}
}

func (hc *HealthChecker) SetReady(ready bool) {
	hc.ready.Store(ready)
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
	if !hc.ready.Load() {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("not ready"))
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ready"))
}
