package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/shiden-guardian/shiden-guardian/internal/model"
)

func TestMetricWithLabel(t *testing.T) {
	raw := `# HELP substrate_block_height height
substrate_block_height{status="best"} 123
substrate_block_height{status="finalized"} 120
substrate_sub_libp2p_peers_count 42
`
	if got := metric(raw, []string{"substrate_block_height"}, `status="finalized"`); got != 120 {
		t.Fatalf("got %v", got)
	}
	if got := metric(raw, []string{"substrate_sub_libp2p_peers_count"}, ""); got != 42 {
		t.Fatalf("got %v", got)
	}
}

func TestRPCHeight(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","result":{"number":"0x2a"},"id":1}`))
	}))
	defer server.Close()
	height, err := rpcHeight(context.Background(), server.URL)
	if err != nil || height != 42 {
		t.Fatalf("height=%d err=%v", height, err)
	}
}

func TestLogSignalsNeverAutoRestart(t *testing.T) {
	signals := logSignals([]model.LogLine{{Cursor: "c1", Timestamp: time.Now(), Priority: "critical", Message: "thread 'main' panicked at database error"}})
	if len(signals) == 0 {
		t.Fatal("expected log signal")
	}
	for _, signal := range signals {
		if signal.AutoCandidate {
			t.Fatalf("log signal %s must not be automatic", signal.Fingerprint)
		}
	}
}

func TestRewardStatusThresholdsAndFailClosed(t *testing.T) {
	base := model.RewardOverview{ActiveSession: true, SchemaOK: true, Quorum: 3}
	if got := rewardStatus(base, 15*time.Minute, 30*time.Minute); got != "healthy" {
		t.Fatalf("healthy=%s", got)
	}
	base.SecondsSinceReward = 15 * 60
	if got := rewardStatus(base, 15*time.Minute, 30*time.Minute); got != "warning" {
		t.Fatalf("warning=%s", got)
	}
	base.SecondsSinceReward = 30 * 60
	if got := rewardStatus(base, 15*time.Minute, 30*time.Minute); got != "critical" {
		t.Fatalf("critical=%s", got)
	}
	base.SchemaOK = false
	if got := rewardStatus(base, 15*time.Minute, 30*time.Minute); got != "degraded" {
		t.Fatalf("schema=%s", got)
	}
	base.SchemaOK = true
	base.Quorum = 1
	if got := rewardStatus(base, 15*time.Minute, 30*time.Minute); got != "degraded" {
		t.Fatalf("quorum=%s", got)
	}
	base.Quorum = 3
	base.ActiveSession = false
	base.SecondsSinceReward = 0
	base.InactiveConfirmations = 1
	if got := rewardStatus(base, 15*time.Minute, 30*time.Minute); got != "verifying" {
		t.Fatalf("single inactive observation=%s", got)
	}
	base.InactiveConfirmations = 2
	if got := rewardStatus(base, 15*time.Minute, 30*time.Minute); got != "critical" {
		t.Fatalf("confirmed inactive=%s", got)
	}
}

func TestRewardChainProgressNeedsBothReferences(t *testing.T) {
	now := time.Now()
	c := controller{externalOK: [2]bool{true, true}, rewardChainProgress: [2]time.Time{now.Add(-time.Minute), now.Add(-time.Minute)}}
	if !c.rewardChainProgressing(now) {
		t.Fatal("two current references were rejected")
	}
	c.rewardChainProgress[1] = now.Add(-3 * time.Minute)
	if c.rewardChainProgressing(now) {
		t.Fatal("stale reference was accepted")
	}
	c.rewardChainProgress[1] = now
	c.externalOK[0] = false
	if c.rewardChainProgressing(now) {
		t.Fatal("unavailable reference was accepted")
	}
}

func TestRewardRPCBackoffIsBounded(t *testing.T) {
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 15 * time.Minute, 15 * time.Minute}
	for index, expected := range want {
		if got := rewardRetryBackoff(index + 1); got != expected {
			t.Fatalf("failure %d backoff=%s want=%s", index+1, got, expected)
		}
	}
}
