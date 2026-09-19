package runner

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

const controlAuthorizationPrefix = "Bearer "

// ControlIdentity contains only the non-secret fields needed to authenticate a
// runner during daemon replacement.
type ControlIdentity struct {
	Service       string `json:"service"`
	InstanceID    string `json:"instanceId"`
	RunnerVersion string `json:"runnerVersion"`
	EngineVersion string `json:"engineVersion"`
}

// Lease tracks when the runner should exit if its supervising daemon disappears.
type Lease struct {
	mu       sync.RWMutex
	duration time.Duration
	deadline time.Time
}

func NewLease(duration time.Duration) *Lease {
	return NewLeaseAt(duration, time.Now())
}

func NewLeaseAt(duration time.Duration, now time.Time) *Lease {
	return &Lease{duration: duration, deadline: now.Add(duration)}
}

func (l *Lease) Renew(now time.Time) {
	l.mu.Lock()
	l.deadline = now.Add(l.duration)
	l.mu.Unlock()
}

func (l *Lease) Deadline() time.Time {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.deadline
}

func (l *Lease) Expired(now time.Time) bool {
	return !now.Before(l.Deadline())
}

type controlHandler struct {
	identity   ControlIdentity
	controlKey string
	lease      *Lease
}

func NewControlHandler(identity ControlIdentity, controlKey string, lease *Lease) http.Handler {
	identity.Service = "ao-accounts-manager"
	return &controlHandler{identity: identity, controlKey: controlKey, lease: lease}
}

func (h *controlHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !validControlAuthorization(r.Header.Get("Authorization"), h.controlKey) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/ao/internal/identity":
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(h.identity)
	case r.Method == http.MethodPost && r.URL.Path == "/ao/internal/lease":
		h.lease.Renew(time.Now())
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func validControlAuthorization(header, want string) bool {
	if !strings.HasPrefix(header, controlAuthorizationPrefix) {
		return false
	}
	got := strings.TrimPrefix(header, controlAuthorizationPrefix)
	if len(got) != len(want) || want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
