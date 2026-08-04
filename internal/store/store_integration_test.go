package store

import (
	"context"
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
	store, err := Open(ctx, url)
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
	if err := store.RewardStats(ctx, &loaded); err != nil {
		t.Fatal(err)
	}
	if loaded.RewardTotalCount < 1 || loaded.LastRewardPlanck != event.CreditedPlanck {
		t.Fatalf("reward stats=%#v", loaded)
	}
	items, err := store.RewardPage(ctx, 10, 0)
	if err != nil || len(items) == 0 || items[0].CreditedPlanck != event.CreditedPlanck {
		t.Fatalf("reward page=%#v err=%v", items, err)
	}
	daily, err := store.RewardDaily(ctx, 2)
	if err != nil || len(daily) == 0 {
		t.Fatalf("reward daily=%#v err=%v", daily, err)
	}
}
