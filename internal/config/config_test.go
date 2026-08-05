package config

import (
	"testing"

	"github.com/shiden-guardian/shiden-guardian/internal/model"
)

func TestRewardConfigurationIsFixedAndOrdered(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://test:test@localhost/test")
	t.Setenv("COLLATOR_REWARD_ADDRESS", model.RewardWallet)
	t.Setenv("EXTERNAL_RPC_URLS", "https://one.invalid,https://two.invalid")
	t.Setenv("REWARD_WARNING_MINUTES", "15")
	t.Setenv("REWARD_CRITICAL_MINUTES", "30")
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}

	t.Setenv("COLLATOR_REWARD_ADDRESS", "wrong")
	if _, err := Load(); err == nil {
		t.Fatal("arbitrary reward address accepted")
	}

	t.Setenv("COLLATOR_REWARD_ADDRESS", model.RewardWallet)
	t.Setenv("REWARD_CRITICAL_MINUTES", "15")
	if _, err := Load(); err == nil {
		t.Fatal("unordered reward thresholds accepted")
	}
}
