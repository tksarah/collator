package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/shiden-guardian/shiden-guardian/internal/model"
)

func TestPublicStatusFromOverview(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		value     model.Overview
		overall   string
		node      string
		sync      string
		finalized *int64
	}{
		{
			name:    "operational",
			value:   model.Overview{Node: model.NodeState{ServiceState: "active"}, Chain: model.ChainState{Status: "healthy", LocalFinalized: 12345}, UpdatedAt: now.Add(-30 * time.Second)},
			overall: "operational", node: "online", sync: "synced", finalized: int64Pointer(12345),
		},
		{
			name:    "exactly sixty seconds is fresh",
			value:   model.Overview{Node: model.NodeState{ServiceState: "active"}, Chain: model.ChainState{Status: "healthy", LocalFinalized: 12345}, UpdatedAt: now.Add(-time.Minute)},
			overall: "operational", node: "online", sync: "synced", finalized: int64Pointer(12345),
		},
		{
			name:    "chain warning",
			value:   model.Overview{Node: model.NodeState{ServiceState: "active"}, Chain: model.ChainState{Status: "warning", LocalFinalized: 12000}, UpdatedAt: now},
			overall: "degraded", node: "online", sync: "catching_up", finalized: int64Pointer(12000),
		},
		{
			name:    "chain state unknown",
			value:   model.Overview{Node: model.NodeState{ServiceState: "active"}, Chain: model.ChainState{Status: "unknown", LocalFinalized: 12000}, UpdatedAt: now},
			overall: "degraded", node: "online", sync: "catching_up", finalized: int64Pointer(12000),
		},
		{
			name:    "known offline service",
			value:   model.Overview{Node: model.NodeState{ServiceState: "failed"}, Chain: model.ChainState{Status: "critical", LocalFinalized: 11900}, UpdatedAt: now},
			overall: "unavailable", node: "offline", sync: "unknown", finalized: int64Pointer(11900),
		},
		{
			name:    "agent state unavailable",
			value:   model.Overview{Node: model.NodeState{ServiceState: "unavailable"}, Chain: model.ChainState{Status: "unknown", LocalFinalized: 11900}, UpdatedAt: now},
			overall: "unavailable", node: "unknown", sync: "unknown", finalized: int64Pointer(11900),
		},
		{
			name:    "stale snapshot hides block",
			value:   model.Overview{Node: model.NodeState{ServiceState: "active"}, Chain: model.ChainState{Status: "healthy", LocalFinalized: 12345}, UpdatedAt: now.Add(-time.Minute - time.Nanosecond)},
			overall: "unavailable", node: "unknown", sync: "unknown",
		},
		{
			name:    "future snapshot hides block",
			value:   model.Overview{Node: model.NodeState{ServiceState: "active"}, Chain: model.ChainState{Status: "healthy", LocalFinalized: 12345}, UpdatedAt: now.Add(time.Nanosecond)},
			overall: "unavailable", node: "unknown", sync: "unknown",
		},
		{
			name:    "missing snapshot time",
			value:   model.Overview{Node: model.NodeState{ServiceState: "active"}, Chain: model.ChainState{Status: "healthy", LocalFinalized: 12345}},
			overall: "unavailable", node: "unknown", sync: "unknown",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := publicStatusFromOverview(test.value, now)
			if got.Network != "shiden" || got.Overall != test.overall || got.Node != test.node || got.Sync != test.sync {
				t.Fatalf("status = %#v", got)
			}
			if !reflect.DeepEqual(got.FinalizedBlock, test.finalized) {
				t.Fatalf("finalized_block = %v, want %v", got.FinalizedBlock, test.finalized)
			}
		})
	}
}

func TestPublicStatusResponseHasOnlyAllowedFields(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	service := newPublicStatusService(func(context.Context) (model.Overview, error) {
		return model.Overview{
			Node:      model.NodeState{Name: "private-node", ServiceState: "active", Version: "private-version", Uptime: 99, RestartCount: 7},
			Chain:     model.ChainState{Status: "healthy", LocalFinalized: 12345, Peers: 42, Lag: 2, ExternalHeight: 12347},
			Host:      model.HostState{CPU: 20, Memory: 30, Disk: 40},
			Rewards:   model.RewardOverview{Address: "private-wallet", WalletFreePlanck: "100"},
			Incidents: []model.Incident{{ID: "private-incident"}},
			UpdatedAt: now,
		}, nil
	})
	service.now = func() time.Time { return now }
	recorder := httptest.NewRecorder()
	service.handle(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/public/status", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status code = %d", recorder.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(body))
	for key := range body {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	want := []string{"finalized_block", "network", "node", "observed_at", "overall", "sync"}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("response keys = %v, want %v", keys, want)
	}
	for _, forbidden := range []string{"private-node", "private-version", "private-wallet", "private-incident"} {
		if contents := recorder.Body.String(); strings.Contains(contents, forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, contents)
		}
	}
	if got := recorder.Header().Get("Cache-Control"); got != "public, max-age=10, must-revalidate" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func TestPublicStatusDatabaseFailureUsesSafeShape(t *testing.T) {
	service := newPublicStatusService(func(context.Context) (model.Overview, error) {
		return model.Overview{}, errors.New("secret database detail")
	})
	recorder := httptest.NewRecorder()
	service.handle(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/public/status", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status code = %d", recorder.Code)
	}
	var got publicNodeStatus
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got != (publicNodeStatus{Network: "shiden", Overall: "unavailable", Node: "unknown", Sync: "unknown"}) {
		t.Fatalf("status = %#v", got)
	}
	if strings.Contains(recorder.Body.String(), "database") || strings.Contains(recorder.Body.String(), "secret") {
		t.Fatalf("database error leaked: %s", recorder.Body.String())
	}
}

func TestPublicStatusUncollectedUsesSafe503(t *testing.T) {
	service := newPublicStatusService(func(context.Context) (model.Overview, error) {
		return model.Overview{}, nil
	})
	recorder := httptest.NewRecorder()
	service.handle(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/public/status", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status code = %d", recorder.Code)
	}
	var got publicNodeStatus
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got != (publicNodeStatus{Network: "shiden", Overall: "unavailable", Node: "unknown", Sync: "unknown"}) {
		t.Fatalf("status = %#v", got)
	}
}

func TestPublicStatusCachesProjection(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	loads := 0
	service := newPublicStatusService(func(context.Context) (model.Overview, error) {
		loads++
		return model.Overview{Node: model.NodeState{ServiceState: "active"}, Chain: model.ChainState{Status: "healthy", LocalFinalized: int64(loads)}, UpdatedAt: now}, nil
	})
	service.now = func() time.Time { return now }
	for range 2 {
		recorder := httptest.NewRecorder()
		service.handle(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/public/status", nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status code = %d", recorder.Code)
		}
	}
	if loads != 1 {
		t.Fatalf("load count = %d, want 1", loads)
	}
}

func TestPublicStatusTrustBoundaryAndRateLimit(t *testing.T) {
	service := newPublicStatusService(func(context.Context) (model.Overview, error) { return model.Overview{}, nil })
	service.lookupProxy = func(context.Context) ([]net.IP, error) { return []net.IP{net.ParseIP("10.0.0.2")}, nil }
	handler := service.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))

	direct := httptest.NewRequest(http.MethodGet, "/api/v1/public/status", nil)
	direct.RemoteAddr = "10.0.0.3:40000"
	direct.Header.Set("X-Real-IP", "203.0.113.8")
	directRecorder := httptest.NewRecorder()
	handler.ServeHTTP(directRecorder, direct)
	if directRecorder.Code != http.StatusForbidden {
		t.Fatalf("direct status = %d", directRecorder.Code)
	}

	invalidIP := httptest.NewRequest(http.MethodGet, "/api/v1/public/status", nil)
	invalidIP.RemoteAddr = "10.0.0.2:40000"
	invalidIP.Header.Set("X-Real-IP", "spoofed")
	invalidRecorder := httptest.NewRecorder()
	handler.ServeHTTP(invalidRecorder, invalidIP)
	if invalidRecorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid client status = %d", invalidRecorder.Code)
	}

	for requestNumber := 1; requestNumber <= 13; requestNumber++ {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/public/status", nil)
		request.RemoteAddr = "10.0.0.2:40000"
		request.Header.Set("X-Real-IP", "203.0.113.8")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if requestNumber <= 12 && recorder.Code != http.StatusNoContent {
			t.Fatalf("request %d status = %d", requestNumber, recorder.Code)
		}
		if requestNumber == 13 {
			if recorder.Code != http.StatusTooManyRequests {
				t.Fatalf("rate-limited status = %d", recorder.Code)
			}
			if recorder.Header().Get("Retry-After") == "" {
				t.Fatal("Retry-After is missing")
			}
		}
	}
}

func TestPublicStatusRouteRejectsPostAndOverviewStillRequiresAuth(t *testing.T) {
	service := newPublicStatusService(func(context.Context) (model.Overview, error) { return model.Overview{}, nil })
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/public/status", http.HandlerFunc(service.handle))
	postRecorder := httptest.NewRecorder()
	mux.ServeHTTP(postRecorder, httptest.NewRequest(http.MethodPost, "/api/v1/public/status", nil))
	if postRecorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d", postRecorder.Code)
	}

	for _, target := range []string{"/api/v1/public/status?verbose=true", "/api/v1/public/status"} {
		var request *http.Request
		if strings.Contains(target, "?") {
			request = httptest.NewRequest(http.MethodGet, target, nil)
		} else {
			request = httptest.NewRequest(http.MethodGet, target, strings.NewReader("{}"))
		}
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("invalid request %q status = %d", target, recorder.Code)
		}
	}

	nextCalled := false
	protected := (&server{}).requireAuth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { nextCalled = true }))
	overviewRecorder := httptest.NewRecorder()
	protected.ServeHTTP(overviewRecorder, httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil))
	if overviewRecorder.Code != http.StatusUnauthorized || nextCalled {
		t.Fatalf("overview status = %d, nextCalled = %v", overviewRecorder.Code, nextCalled)
	}
}

func int64Pointer(value int64) *int64 { return &value }
