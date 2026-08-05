package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/shiden-guardian/shiden-guardian/internal/model"
)

func TestPostgresMigrations(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := OpenAndMigrate(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	if err := store.SetSetting(ctx, "integration_test", true); err != nil {
		t.Fatal(err)
	}
	if !store.SettingBool(ctx, "integration_test", false) {
		t.Fatal("setting round-trip failed")
	}
	if err := DryRunMigrations(ctx, url); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	overview := model.RewardOverview{Address: model.RewardWallet, Status: "collecting", MonitoringStartedAt: now, LastScannedBlock: 12345, Reward24hPlanck: "0", RewardTotalPlanck: "0", LastRewardPlanck: "0", Sources: map[string]string{"local": "ok"}}
	if err := store.SaveRewardOverview(ctx, overview); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadRewardOverview(ctx)
	if err != nil || loaded.LastScannedBlock != overview.LastScannedBlock || loaded.Address != model.RewardWallet {
		t.Fatalf("reward state=%#v err=%v", loaded, err)
	}
	event := model.RewardObservation{BlockNumber: 12345, BlockHash: "0xstore-integration-12345", AuthoredAt: now, ExpectedPlanck: "241000000000000000", CreditedPlanck: "241000000000000000", WalletBeforePlanck: "6278000000000000000000", WalletAfterPlanck: "6278241000000000000000", PotBeforePlanck: "482000000001000000", Verification: "confirmed", SourceCount: 3, Evidence: map[string]any{"local": "matched"}}
	if err := store.SaveRewardObservation(ctx, event); err != nil {
		t.Fatal(err)
	}
	atomicOverview := loaded
	atomicOverview.LastScannedBlock = 12347
	atomicEvents := []model.RewardObservation{
		{BlockNumber: 12346, BlockHash: "0xstore-integration-12346", AuthoredAt: now.Add(time.Second), ExpectedPlanck: "100", CreditedPlanck: "100", WalletBeforePlanck: "1000", WalletAfterPlanck: "1100", PotBeforePlanck: "201", Verification: "confirmed", SourceCount: 1, Evidence: map[string]any{"local": "matched"}},
		{BlockNumber: 12347, BlockHash: "0xstore-integration-12347", AuthoredAt: now.Add(2 * time.Second), ExpectedPlanck: "100", CreditedPlanck: "100", WalletBeforePlanck: "1100", WalletAfterPlanck: "1200", PotBeforePlanck: "201", Verification: "confirmed", SourceCount: 1, Evidence: map[string]any{"local": "matched"}},
	}
	if err := store.ApplyRewardScan(ctx, 12345, atomicOverview, atomicEvents); err != nil {
		t.Fatalf("atomic reward scan: %v", err)
	}
	loaded, err = store.LoadRewardOverview(ctx)
	if err != nil || loaded.LastScannedBlock != 12347 {
		t.Fatalf("atomic cursor=%d err=%v", loaded.LastScannedBlock, err)
	}

	if err := store.ApplyRewardScan(ctx, 12345, atomicOverview, atomicEvents); !errors.Is(err, ErrRewardCursorConflict) {
		t.Fatalf("idempotent reward retry err=%v", err)
	}
	var duplicateCount int
	if err := store.DB.QueryRowContext(ctx, `SELECT count(*) FROM reward_events WHERE block_number=12346`).Scan(&duplicateCount); err != nil || duplicateCount != 1 {
		t.Fatalf("duplicate count=%d err=%v", duplicateCount, err)
	}

	rollbackOverview := loaded
	rollbackOverview.LastScannedBlock = 12349
	rollbackEvents := []model.RewardObservation{
		{BlockNumber: 12348, BlockHash: "0xstore-integration-12348", AuthoredAt: now.Add(3 * time.Second), ExpectedPlanck: "100", CreditedPlanck: "100", WalletBeforePlanck: "1200", WalletAfterPlanck: "1300", PotBeforePlanck: "201", Verification: "confirmed", SourceCount: 1},
		{BlockNumber: 12349, BlockHash: "0xstore-integration-12349", AuthoredAt: now.Add(4 * time.Second), ExpectedPlanck: "not-numeric", CreditedPlanck: "100", WalletBeforePlanck: "1300", WalletAfterPlanck: "1400", PotBeforePlanck: "201", Verification: "confirmed", SourceCount: 1},
	}
	if err := store.ApplyRewardScan(ctx, 12347, rollbackOverview, rollbackEvents); err == nil {
		t.Fatal("invalid numeric reward scan unexpectedly committed")
	}
	loaded, err = store.LoadRewardOverview(ctx)
	if err != nil || loaded.LastScannedBlock != 12347 {
		t.Fatalf("rollback cursor=%d err=%v", loaded.LastScannedBlock, err)
	}
	var rolledBackCount int
	if err := store.DB.QueryRowContext(ctx, `SELECT count(*) FROM reward_events WHERE block_number=12348`).Scan(&rolledBackCount); err != nil || rolledBackCount != 0 {
		t.Fatalf("rolled back event count=%d err=%v", rolledBackCount, err)
	}

	conflictOverview := loaded
	conflictOverview.LastScannedBlock = 12348
	if err := store.ApplyRewardScan(ctx, 12346, conflictOverview, nil); !errors.Is(err, ErrRewardCursorConflict) {
		t.Fatalf("cursor conflict err=%v", err)
	}

	existingConflictEvent := model.RewardObservation{BlockNumber: 12348, BlockHash: "0xoriginal-hash-for-12348", AuthoredAt: now.Add(5 * time.Second), ExpectedPlanck: "100", CreditedPlanck: "100", WalletBeforePlanck: "1200", WalletAfterPlanck: "1300", PotBeforePlanck: "201", Verification: "confirmed", SourceCount: 1}
	if err := store.SaveRewardObservation(ctx, existingConflictEvent); err != nil {
		t.Fatalf("prepare block hash conflict: %v", err)
	}
	hashConflictOverview := loaded
	hashConflictOverview.LastScannedBlock = 12348
	hashConflictEvents := []model.RewardObservation{
		{BlockNumber: 12348, BlockHash: "0xdifferent-hash-for-12348", AuthoredAt: now.Add(6 * time.Second), ExpectedPlanck: "100", CreditedPlanck: "100", WalletBeforePlanck: "1200", WalletAfterPlanck: "1300", PotBeforePlanck: "201", Verification: "confirmed", SourceCount: 1},
	}
	if err := store.ApplyRewardScan(ctx, 12347, hashConflictOverview, hashConflictEvents); !errors.Is(err, ErrRewardBlockHashConflict) {
		t.Fatalf("block hash conflict err=%v", err)
	}
	loaded, err = store.LoadRewardOverview(ctx)
	if err != nil || loaded.LastScannedBlock != 12347 {
		t.Fatalf("hash conflict cursor=%d err=%v", loaded.LastScannedBlock, err)
	}
	var conflictHash string
	if err := store.DB.QueryRowContext(ctx, `SELECT block_hash FROM reward_events WHERE block_number=12348`).Scan(&conflictHash); err != nil || conflictHash != existingConflictEvent.BlockHash {
		t.Fatalf("hash conflict changed existing event hash=%q err=%v", conflictHash, err)
	}
	if err := store.RewardStats(ctx, &loaded); err != nil {
		t.Fatal(err)
	}
	if loaded.RewardTotalCount < 4 || loaded.LastRewardPlanck != existingConflictEvent.CreditedPlanck {
		t.Fatalf("reward stats=%#v", loaded)
	}
	items, err := store.RewardPage(ctx, 10, 0)
	if err != nil || len(items) == 0 || items[0].BlockHash != existingConflictEvent.BlockHash {
		t.Fatalf("reward page=%#v err=%v", items, err)
	}
	daily, err := store.RewardDaily(ctx, 2)
	if err != nil || len(daily) == 0 {
		t.Fatalf("reward daily=%#v err=%v", daily, err)
	}

	var actionsBefore, eventsBefore int
	if err := store.DB.QueryRowContext(ctx, `SELECT count(*) FROM remediation_actions`).Scan(&actionsBefore); err != nil {
		t.Fatal(err)
	}
	if err := store.DB.QueryRowContext(ctx, `SELECT count(*) FROM reward_events`).Scan(&eventsBefore); err != nil {
		t.Fatal(err)
	}
	rebase, err := store.AcknowledgePrunedRewardGap(ctx, 12347, 12447, 12511)
	if err != nil {
		t.Fatalf("acknowledge pruned reward gap: %v", err)
	}
	if rebase.FromBlock != 12348 || rebase.ThroughBlock != 12447 || rebase.ResumeFromBlock != 12448 || rebase.AuditID == "" {
		t.Fatalf("rebase=%#v", rebase)
	}
	loaded, err = store.LoadRewardOverview(ctx)
	if err != nil || loaded.LastScannedBlock != 12447 || loaded.Sources["historical_gap"] != "acknowledged_pruned_blocks_12348_12447" || loaded.Gap == "" {
		t.Fatalf("rebased overview=%#v err=%v", loaded, err)
	}
	var auditAction, auditActor, auditResult, auditReason, auditRecovered string
	if err := store.DB.QueryRowContext(ctx, `SELECT action,actor,result,details->>'reason',details->>'history_recovered' FROM audit_events WHERE id=$1`, rebase.AuditID).Scan(&auditAction, &auditActor, &auditResult, &auditReason, &auditRecovered); err != nil {
		t.Fatal(err)
	}
	if auditAction != "reward.history_gap.acknowledge" || auditActor != "operator-approved-controller-recovery" || auditResult != "acknowledged" || auditReason != "local_state_pruned" || auditRecovered != "false" {
		t.Fatalf("audit=%q %q %q %q %q", auditAction, auditActor, auditResult, auditReason, auditRecovered)
	}
	var actionsAfter, eventsAfter int
	if err := store.DB.QueryRowContext(ctx, `SELECT count(*) FROM remediation_actions`).Scan(&actionsAfter); err != nil {
		t.Fatal(err)
	}
	if err := store.DB.QueryRowContext(ctx, `SELECT count(*) FROM reward_events`).Scan(&eventsAfter); err != nil {
		t.Fatal(err)
	}
	if actionsAfter != actionsBefore || eventsAfter != eventsBefore {
		t.Fatalf("rebase changed actions/events: actions %d->%d events %d->%d", actionsBefore, actionsAfter, eventsBefore, eventsAfter)
	}
	if _, err := store.AcknowledgePrunedRewardGap(ctx, 12347, 12447, 12511); !errors.Is(err, ErrRewardCursorConflict) {
		t.Fatalf("replayed rebase err=%v", err)
	}
	var auditCount int
	if err := store.DB.QueryRowContext(ctx, `SELECT count(*) FROM audit_events WHERE action='reward.history_gap.acknowledge'`).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("rebase audit count=%d err=%v", auditCount, err)
	}
}
