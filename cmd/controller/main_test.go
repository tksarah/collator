package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shiden-guardian/shiden-guardian/internal/model"
	"github.com/shiden-guardian/shiden-guardian/internal/reward"
	"github.com/shiden-guardian/shiden-guardian/internal/store"
)

type fakeRewardScanner struct {
	calls int
	err   error
}

func (f *fakeRewardScanner) RewardScan(_ context.Context, from, to int64) (model.RewardScan, error) {
	f.calls++
	if f.err != nil {
		return model.RewardScan{}, f.err
	}
	return model.RewardScan{Source: "local", From: from, To: to, Observations: []model.RewardObservation{}}, nil
}

type fakeRewardWriter struct {
	calls          int
	expectedCursor int64
	writtenCursor  int64
	err            error
}

type fakeRewardGapAuthority struct {
	snapshot  model.RewardSnapshot
	scanCalls [][2]int64
	oldErr    error
	probeErr  error
}

func (f *fakeRewardGapAuthority) RewardSnapshot(context.Context) (model.RewardSnapshot, error) {
	return f.snapshot, nil
}

func (f *fakeRewardGapAuthority) RewardScan(_ context.Context, from, to int64) (model.RewardScan, error) {
	f.scanCalls = append(f.scanCalls, [2]int64{from, to})
	if len(f.scanCalls) == 1 {
		if f.oldErr != nil {
			return model.RewardScan{}, f.oldErr
		}
		return model.RewardScan{Source: "local", From: from, To: to}, nil
	}
	if f.probeErr != nil {
		return model.RewardScan{}, f.probeErr
	}
	return model.RewardScan{Source: "local", From: from, To: to, Observations: []model.RewardObservation{{BlockNumber: to, BlockHash: "0xprobe", Verification: "confirmed_extra", SourceCount: 1}}}, nil
}

type fakeRewardGapWriter struct {
	calls     int
	expected  int64
	baseline  int64
	finalized int64
	err       error
}

func (f *fakeRewardGapWriter) AcknowledgePrunedRewardGap(_ context.Context, expected, baseline, finalized int64) (store.RewardGapRebase, error) {
	f.calls++
	f.expected, f.baseline, f.finalized = expected, baseline, finalized
	return store.RewardGapRebase{AuditID: "aud_test", FromBlock: expected + 1, ThroughBlock: baseline, ResumeFromBlock: baseline + 1, SnapshotFinalizedBlock: finalized}, f.err
}

func (f *fakeRewardWriter) ApplyRewardScan(_ context.Context, expected int64, overview model.RewardOverview, _ []model.RewardObservation) error {
	f.calls++
	f.expectedCursor = expected
	f.writtenCursor = overview.LastScannedBlock
	return f.err
}

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

func TestLocalRewardHistoryIsBoundedAndRateLimited(t *testing.T) {
	scanner := &fakeRewardScanner{}
	writer := &fakeRewardWriter{}
	c := controller{rewardScanner: scanner, rewardWriter: writer}
	now := time.Unix(1_800_000_000, 0)
	previous := model.RewardOverview{LastScannedBlock: 100}

	updated, attempted, err := c.scanLocalRewardHistory(context.Background(), now, previous, 200)
	if err != nil || !attempted {
		t.Fatalf("first scan attempted=%v err=%v", attempted, err)
	}
	if updated.LastScannedBlock != 116 || scanner.calls != 1 || writer.calls != 1 || writer.expectedCursor != 100 || writer.writtenCursor != 116 {
		t.Fatalf("first scan updated=%#v scanner=%d writer=%#v", updated, scanner.calls, writer)
	}
	if updated.Gap == "" {
		t.Fatal("catch-up gap was cleared before reaching finalized")
	}

	deferred, attempted, err := c.scanLocalRewardHistory(context.Background(), now.Add(14*time.Second), updated, 200)
	if err != nil || attempted || deferred.LastScannedBlock != 116 || scanner.calls != 1 {
		t.Fatalf("early retry attempted=%v cursor=%d calls=%d err=%v", attempted, deferred.LastScannedBlock, scanner.calls, err)
	}

	updated, attempted, err = c.scanLocalRewardHistory(context.Background(), now.Add(15*time.Second), updated, 200)
	if err != nil || !attempted || updated.LastScannedBlock != 132 || scanner.calls != 2 {
		t.Fatalf("second scan attempted=%v cursor=%d calls=%d err=%v", attempted, updated.LastScannedBlock, scanner.calls, err)
	}
}

func TestLocalRewardFailureDoesNotUseExternalOrAdvanceCursor(t *testing.T) {
	var externalCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		externalCalls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	monitor, err := reward.NewMonitor(model.RewardWallet, "external_1", reward.NewHTTPRPC(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	scanner := &fakeRewardScanner{err: errors.New("local timeout")}
	writer := &fakeRewardWriter{}
	c := controller{rewardScanner: scanner, rewardWriter: writer, rewardExternal: []*reward.Monitor{monitor}}
	now := time.Unix(1_800_000_000, 0)
	previous := model.RewardOverview{LastScannedBlock: 500}

	updated, attempted, err := c.scanLocalRewardHistory(context.Background(), now, previous, 600)
	if err == nil || !attempted || updated.LastScannedBlock != 500 || writer.calls != 0 || externalCalls.Load() != 0 {
		t.Fatalf("failed scan updated=%#v attempted=%v writer=%d external=%d err=%v", updated, attempted, writer.calls, externalCalls.Load(), err)
	}
	_, attempted, _ = c.scanLocalRewardHistory(context.Background(), now.Add(14*time.Second), previous, 600)
	if attempted || scanner.calls != 1 {
		t.Fatalf("failure backoff was bypassed attempted=%v calls=%d", attempted, scanner.calls)
	}
	_, attempted, _ = c.scanLocalRewardHistory(context.Background(), now.Add(15*time.Second), previous, 600)
	if !attempted || scanner.calls != 2 {
		t.Fatalf("failure retry did not run attempted=%v calls=%d", attempted, scanner.calls)
	}
}

func TestLocalRewardWriterFailureDoesNotAdvanceCursor(t *testing.T) {
	scanner := &fakeRewardScanner{}
	writer := &fakeRewardWriter{err: errors.New("database unavailable")}
	c := controller{rewardScanner: scanner, rewardWriter: writer}
	previous := model.RewardOverview{LastScannedBlock: 700}
	updated, attempted, err := c.scanLocalRewardHistory(context.Background(), time.Now(), previous, 800)
	if err == nil || !attempted || updated.LastScannedBlock != previous.LastScannedBlock {
		t.Fatalf("writer failure updated=%#v attempted=%v err=%v", updated, attempted, err)
	}
}

func TestRewardScanBackoffIsBounded(t *testing.T) {
	want := []time.Duration{15 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 5 * time.Minute, 5 * time.Minute}
	for index, expected := range want {
		if got := rewardScanRetryBackoff(index + 1); got != expected {
			t.Fatalf("failure %d backoff=%s want=%s", index+1, got, expected)
		}
	}
}

func TestLocalRewardScanRejectsNonLocalEvidence(t *testing.T) {
	for _, verification := range []string{"confirmed", "confirmed_extra", "insufficient", "pot_empty"} {
		base := model.RewardScan{
			From:   11,
			To:     12,
			Source: "local",
			Observations: []model.RewardObservation{{
				BlockNumber:  12,
				BlockHash:    "0x12",
				Verification: verification,
				SourceCount:  1,
			}},
		}
		if err := validateLocalRewardScan(base, 11, 12); err != nil {
			t.Fatalf("valid local scan verification=%s: %v", verification, err)
		}
	}
	base := model.RewardScan{From: 11, To: 12, Source: "local", Observations: []model.RewardObservation{{BlockNumber: 12, BlockHash: "0x12", Verification: "confirmed", SourceCount: 1}}}
	wrongSource := base
	wrongSource.Source = "external_1"
	if err := validateLocalRewardScan(wrongSource, 11, 12); err == nil {
		t.Fatal("external scan passed local validation")
	}
	wrongCount := base
	wrongCount.Observations = append([]model.RewardObservation(nil), base.Observations...)
	wrongCount.Observations[0].SourceCount = 2
	if err := validateLocalRewardScan(wrongCount, 11, 12); err == nil {
		t.Fatal("pre-quorum observation passed local validation")
	}
}

func TestPrunedRewardGapAcknowledgementRequiresDiscardedAndRetainedLocalState(t *testing.T) {
	authority := &fakeRewardGapAuthority{
		snapshot: model.RewardSnapshot{Source: "local", SchemaOK: true, FinalizedBlock: 1000, FinalizedHash: "0xfinalized"},
		oldErr:   errors.New("rpc 4003: State already discarded for old block"),
	}
	writer := &fakeRewardGapWriter{}
	result, err := acknowledgePrunedRewardGap(context.Background(), authority, writer, 800)
	if err != nil {
		t.Fatal(err)
	}
	if len(authority.scanCalls) != 2 || authority.scanCalls[0] != [2]int64{801, 816} || authority.scanCalls[1] != [2]int64{937, 952} {
		t.Fatalf("scan calls=%v", authority.scanCalls)
	}
	if writer.calls != 1 || writer.expected != 800 || writer.baseline != 936 || writer.finalized != 1000 {
		t.Fatalf("writer=%#v", writer)
	}
	if result.FromBlock != 801 || result.ThroughBlock != 936 || result.ResumeFromBlock != 937 {
		t.Fatalf("result=%#v", result)
	}
}

func TestPrunedRewardGapAcknowledgementRefusesUnsafeEvidence(t *testing.T) {
	tests := []struct {
		name     string
		snapshot model.RewardSnapshot
		oldErr   error
		probeErr error
	}{
		{name: "non-local snapshot", snapshot: model.RewardSnapshot{Source: "external_1", SchemaOK: true, FinalizedBlock: 1000, FinalizedHash: "0xfinalized"}, oldErr: errors.New("State already discarded")},
		{name: "readable old history", snapshot: model.RewardSnapshot{Source: "local", SchemaOK: true, FinalizedBlock: 1000, FinalizedHash: "0xfinalized"}},
		{name: "unexpected old failure", snapshot: model.RewardSnapshot{Source: "local", SchemaOK: true, FinalizedBlock: 1000, FinalizedHash: "0xfinalized"}, oldErr: errors.New("connection refused")},
		{name: "retained scan failure", snapshot: model.RewardSnapshot{Source: "local", SchemaOK: true, FinalizedBlock: 1000, FinalizedHash: "0xfinalized"}, oldErr: errors.New("State already discarded"), probeErr: errors.New("retained unavailable")},
		{name: "within retained window", snapshot: model.RewardSnapshot{Source: "local", SchemaOK: true, FinalizedBlock: 850, FinalizedHash: "0xfinalized"}, oldErr: errors.New("State already discarded")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authority := &fakeRewardGapAuthority{snapshot: test.snapshot, oldErr: test.oldErr, probeErr: test.probeErr}
			writer := &fakeRewardGapWriter{}
			if _, err := acknowledgePrunedRewardGap(context.Background(), authority, writer, 800); err == nil {
				t.Fatal("unsafe acknowledgement succeeded")
			}
			if writer.calls != 0 {
				t.Fatalf("unsafe acknowledgement reached writer: %#v", writer)
			}
		})
	}
}

func TestExternalReward429UsesSharedBackoff(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	monitor, err := reward.NewMonitor(model.RewardWallet, "external_1", reward.NewHTTPRPC(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	c := controller{rewardExternal: []*reward.Monitor{monitor}}
	observation := model.RewardObservation{BlockNumber: 42, BlockHash: "0x42", Verification: "insufficient"}
	c.confirmRewardAnomaly(context.Background(), &observation)
	if calls.Load() != 1 || c.rewardExternalFails[0] != 1 || c.rewardExternalRetry[0].IsZero() {
		t.Fatalf("first failure calls=%d failures=%d retry=%v", calls.Load(), c.rewardExternalFails[0], c.rewardExternalRetry[0])
	}
	c.confirmRewardAnomaly(context.Background(), &observation)
	if calls.Load() != 1 {
		t.Fatalf("backoff was bypassed calls=%d", calls.Load())
	}
}

func TestRewardSignalsAreNeverAutomatic(t *testing.T) {
	for _, signal := range []struct {
		fingerprint string
		severity    string
	}{
		{"reward-monitor-degraded", "warning"},
		{"reward-active-set-missing", "critical"},
		{"reward-credit-mismatch", "critical"},
		{"reward-silence", "warning"},
		{"reward-silence", "critical"},
	} {
		got := rewardSignal(signal.fingerprint, signal.severity, "test", map[string]any{"test": true})
		if got.AutoCandidate {
			t.Fatalf("reward signal %s/%s became automatic", signal.fingerprint, signal.severity)
		}
	}
}
