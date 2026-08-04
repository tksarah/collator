//go:build !windows

package agentclient

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/shiden-guardian/shiden-guardian/internal/model"
)

func TestUnixAgentClient(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "agent.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/snapshot", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(model.AgentSnapshot{Node: model.NodeState{Name: "tk_sdn_collator", ServiceState: "active"}})
	})
	mux.HandleFunc("GET /v1/metrics", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("guardian_node_up 1\n")) })
	mux.HandleFunc("GET /v1/rewards/snapshot", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(model.RewardSnapshot{Address: model.RewardWallet, FinalizedBlock: 100, LastAuthoredBlock: 99, ActiveSession: true})
	})
	mux.HandleFunc("GET /v1/rewards/scan", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(model.RewardScan{From: 99, To: 100, Source: "local", Observations: []model.RewardObservation{{BlockNumber: 99, Verification: "confirmed"}}})
	})
	mux.HandleFunc("POST /v1/restart", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "succeeded"})
	})
	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	t.Cleanup(func() { _ = server.Close() })
	client := New(socket, socket)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	snapshot, err := client.Snapshot(ctx)
	if err != nil || snapshot.Node.ServiceState != "active" {
		t.Fatalf("snapshot: %#v %v", snapshot, err)
	}
	metrics, err := client.Metrics(ctx)
	if err != nil || metrics != "guardian_node_up 1\n" {
		t.Fatalf("metrics: %q %v", metrics, err)
	}
	rewardSnapshot, err := client.RewardSnapshot(ctx)
	if err != nil || !rewardSnapshot.ActiveSession || rewardSnapshot.LastAuthoredBlock != 99 {
		t.Fatalf("reward snapshot: %#v %v", rewardSnapshot, err)
	}
	rewardScan, err := client.RewardScan(ctx, 99, 100)
	if err != nil || len(rewardScan.Observations) != 1 {
		t.Fatalf("reward scan: %#v %v", rewardScan, err)
	}
	result, err := client.Restart(ctx, "act_test", "integration test reason")
	if err != nil || result["status"] != "succeeded" {
		t.Fatalf("restart: %#v %v", result, err)
	}
}
