package reward

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/shiden-guardian/shiden-guardian/internal/model"
)

func TestLiveShidenRewardSnapshot(t *testing.T) {
	url := os.Getenv("SHIDEN_RPC_URL")
	if url == "" {
		t.Skip("SHIDEN_RPC_URL is not set")
	}
	monitor, err := NewMonitor(model.RewardWallet, "integration", NewHTTPRPC(url))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	snapshot, err := monitor.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Address != model.RewardWallet || snapshot.FinalizedBlock <= 0 || snapshot.LastAuthoredBlock <= 0 {
		t.Fatalf("snapshot=%#v", snapshot)
	}
	if snapshot.SpecVersion != CurrentSpecVersion || !IsSupportedSpecVersion(snapshot.SpecVersion) || !snapshot.SchemaOK {
		t.Fatalf("unsupported runtime snapshot=%#v", snapshot)
	}
	if snapshot.ValidatorCount <= 0 || !snapshot.ActiveSession {
		t.Fatalf("collator is not active: %#v", snapshot)
	}
	from := snapshot.FinalizedBlock - 2
	if _, err := monitor.Scan(ctx, from, snapshot.FinalizedBlock); err != nil {
		t.Fatalf("recent scan %d-%d: %v", from, snapshot.FinalizedBlock, err)
	}
}
