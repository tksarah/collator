package main

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shiden-guardian/shiden-guardian/internal/authlimit"
	"github.com/shiden-guardian/shiden-guardian/internal/model"
)

const (
	publicStatusFreshness = time.Minute
	publicStatusCacheTTL  = 10 * time.Second
)

type publicNodeStatus struct {
	Network        string     `json:"network"`
	Overall        string     `json:"overall"`
	Node           string     `json:"node"`
	Sync           string     `json:"sync"`
	FinalizedBlock *int64     `json:"finalized_block"`
	ObservedAt     *time.Time `json:"observed_at"`
}

type publicStatusCache struct {
	sync.Mutex
	value      publicNodeStatus
	statusCode int
	expiresAt  time.Time
	valid      bool
}

type publicStatusService struct {
	load        func(context.Context) (model.Overview, error)
	lookupProxy func(context.Context) ([]net.IP, error)
	limiter     *authlimit.Limiter
	now         func() time.Time
	cache       publicStatusCache
}

func newPublicStatusService(load func(context.Context) (model.Overview, error)) *publicStatusService {
	limiter := authlimit.New(authlimit.Config{
		GlobalBurst:     60,
		GlobalRefill:    100 * time.Millisecond,
		IPBurst:         12,
		IPRefill:        5 * time.Second,
		MaxIPs:          4096,
		MaxAccounts:     1,
		EntryTTL:        30 * time.Minute,
		CleanupInterval: time.Minute,
	})
	return &publicStatusService{
		load:    load,
		limiter: limiter,
		now:     time.Now,
		lookupProxy: func(ctx context.Context) ([]net.IP, error) {
			return net.DefaultResolver.LookupIP(ctx, "ip", "caddy")
		},
	}
}

func unavailablePublicStatus(observedAt *time.Time) publicNodeStatus {
	return publicNodeStatus{
		Network:    "shiden",
		Overall:    "unavailable",
		Node:       "unknown",
		Sync:       "unknown",
		ObservedAt: observedAt,
	}
}

func publicStatusFromOverview(value model.Overview, now time.Time) publicNodeStatus {
	var observedAt *time.Time
	if !value.UpdatedAt.IsZero() {
		observed := value.UpdatedAt.UTC()
		observedAt = &observed
	}
	result := unavailablePublicStatus(observedAt)
	if observedAt == nil {
		return result
	}
	age := now.Sub(*observedAt)
	if age < 0 || age > publicStatusFreshness {
		return result
	}
	if value.Chain.LocalFinalized > 0 {
		finalized := value.Chain.LocalFinalized
		result.FinalizedBlock = &finalized
	}
	if value.Node.ServiceState != "active" {
		switch value.Node.ServiceState {
		case "inactive", "failed", "deactivating":
			result.Node = "offline"
		}
		return result
	}
	result.Node = "online"
	if value.Chain.Status == "healthy" {
		result.Overall = "operational"
		result.Sync = "synced"
		return result
	}
	result.Overall = "degraded"
	result.Sync = "catching_up"
	return result
}

func (s *publicStatusService) status(ctx context.Context) (int, publicNodeStatus) {
	now := s.now()
	s.cache.Lock()
	defer s.cache.Unlock()
	if s.cache.valid && now.Before(s.cache.expiresAt) {
		return s.cache.statusCode, s.cache.value
	}

	queryContext, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	value, err := s.load(queryContext)
	statusCode := http.StatusOK
	status := publicStatusFromOverview(value, now)
	if err != nil || value.UpdatedAt.IsZero() {
		statusCode = http.StatusServiceUnavailable
		status = unavailablePublicStatus(nil)
	}

	s.cache.value = status
	s.cache.statusCode = statusCode
	s.cache.expiresAt = now.Add(publicStatusCacheTTL)
	s.cache.valid = true
	return statusCode, status
}

func (s *publicStatusService) handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" || r.ContentLength != 0 {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	statusCode, status := s.status(r.Context())
	w.Header().Set("Cache-Control", "public, max-age=10, must-revalidate")
	writeJSON(w, statusCode, status)
}

func (s *publicStatusService) middleware(next http.Handler) http.Handler {
	return s.caddyOnly(s.rateLimit(next))
}

func (s *publicStatusService) caddyOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			s.publicError(w, http.StatusForbidden, "untrusted_proxy")
			return
		}
		peer := net.ParseIP(host)
		if peer == nil || s.lookupProxy == nil {
			s.publicError(w, http.StatusForbidden, "untrusted_proxy")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		addresses, lookupErr := s.lookupProxy(ctx)
		if lookupErr == nil {
			for _, address := range addresses {
				if peer.Equal(address) {
					next.ServeHTTP(w, r)
					return
				}
			}
		}
		s.publicError(w, http.StatusForbidden, "untrusted_proxy")
	})
}

func (s *publicStatusService) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clientIP := strings.TrimSpace(r.Header.Get("X-Real-IP"))
		if net.ParseIP(clientIP) == nil {
			s.publicError(w, http.StatusBadRequest, "invalid_client_address")
			return
		}
		decision := s.limiter.Allow(clientIP, "public-status")
		if !decision.Allowed {
			seconds := int64((decision.RetryAfter + time.Second - 1) / time.Second)
			if seconds < 1 {
				seconds = 1
			}
			w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
			s.publicError(w, http.StatusTooManyRequests, "too_many_requests")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *publicStatusService) publicError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, map[string]string{"error": code})
}
