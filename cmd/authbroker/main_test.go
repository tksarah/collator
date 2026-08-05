package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/shiden-guardian/shiden-guardian/internal/authlimit"
)

func TestLoginRateLimitReturnsRetryAfterBeforeDatabase(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cfg := authlimit.DefaultConfig()
	cfg.GlobalBurst = 1
	cfg.IPBurst = 1
	limiter := authlimit.NewWithClock(cfg, func() time.Time { return now })
	if !limiter.Allow("192.0.2.10", "first").Allowed {
		t.Fatal("initial limiter token missing")
	}
	s := &server{limiter: limiter}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(`{"username":"different","password":"not-used","totp_code":"000000"}`))
	request.Header.Set("X-Real-IP", "192.0.2.10")
	response := httptest.NewRecorder()
	s.login(response, request)
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if response.Header().Get("Retry-After") != "90" {
		t.Fatalf("Retry-After=%q", response.Header().Get("Retry-After"))
	}
}

func TestCaddyOnlyAcceptsResolvedPeerAndLocalHealth(t *testing.T) {
	s := &server{lookupProxy: func(context.Context) ([]net.IP, error) {
		return []net.IP{net.ParseIP("172.31.0.10")}, nil
	}}
	handler := s.caddyOnly(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))

	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	request.RemoteAddr = "172.31.0.10:43210"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("resolved Caddy peer status=%d", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.RemoteAddr = "127.0.0.1:43211"
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("local health status=%d", response.Code)
	}
}

func TestCaddyOnlyRejectsDatabaseNetworkPeer(t *testing.T) {
	s := &server{lookupProxy: func(context.Context) ([]net.IP, error) {
		return []net.IP{net.ParseIP("172.31.0.10")}, nil
	}}
	handler := s.caddyOnly(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/bootstrap/status", nil)
	request.RemoteAddr = "172.32.0.20:43210"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("database peer status=%d", response.Code)
	}
}

func TestLoginRejectsMissingTrustedClientAddress(t *testing.T) {
	s := &server{limiter: authlimit.New(authlimit.DefaultConfig())}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(`{"username":"admin","password":"not-used","totp_code":"000000"}`))
	request.Header.Set("X-Real-IP", "attacker-controlled")
	response := httptest.NewRecorder()
	s.login(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestPasswordHashConcurrencyIsBounded(t *testing.T) {
	s := &server{hashGate: make(chan struct{}, 2)}
	if !s.acquireHash() || !s.acquireHash() {
		t.Fatal("expected two hash slots")
	}
	if s.acquireHash() {
		t.Fatal("third hash slot was accepted")
	}
	s.releaseHash()
	if !s.acquireHash() {
		t.Fatal("released hash slot was not reusable")
	}
}
