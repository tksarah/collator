package authlimit

import (
	"fmt"
	"testing"
	"time"
)

func TestLimiterBoundsAndUniqueUsersCannotBypassIP(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	clock := func() time.Time { return now }
	cfg := DefaultConfig()
	cfg.GlobalBurst = 200_000
	cfg.IPBurst = 10
	limiter := NewWithClock(cfg, clock)
	for index := 0; index < 100_000; index++ {
		decision := limiter.Allow(fmt.Sprintf("192.0.2.%d", index), fmt.Sprintf("user-%d", index))
		if !decision.Allowed {
			t.Fatalf("unique entry %d unexpectedly denied", index)
		}
	}
	ips, accounts := limiter.Counts()
	if ips != cfg.MaxIPs || accounts != cfg.MaxAccounts {
		t.Fatalf("counts=(%d,%d), want=(%d,%d)", ips, accounts, cfg.MaxIPs, cfg.MaxAccounts)
	}

	limiter = NewWithClock(DefaultConfig(), clock)
	for index := 0; index < 10; index++ {
		if !limiter.Allow("198.51.100.9", fmt.Sprintf("different-%d", index)).Allowed {
			t.Fatalf("attempt %d denied too early", index)
		}
	}
	if limiter.Allow("198.51.100.9", "another-user").Allowed {
		t.Fatal("changing usernames bypassed the IP bucket")
	}
	if retry := limiter.Allow("198.51.100.9", "yet-another-user").RetryAfter; retry != 90*time.Second {
		t.Fatalf("IP RetryAfter=%s, want 90s", retry)
	}
}

func TestLimiterGlobalRetryAfter(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cfg := DefaultConfig()
	cfg.GlobalBurst = 1
	limiter := NewWithClock(cfg, func() time.Time { return now })
	if !limiter.Allow("192.0.2.1", "first").Allowed {
		t.Fatal("first attempt denied")
	}
	decision := limiter.Allow("192.0.2.2", "second")
	if decision.Allowed || decision.RetryAfter != time.Second {
		t.Fatalf("decision=%+v", decision)
	}
}

func TestLimiterAccountDelayNeverHardLocksAnotherIP(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	limiter := NewWithClock(DefaultConfig(), func() time.Time { return now })
	for index := 0; index < 8; index++ {
		limiter.Fail("admin")
	}
	decision := limiter.Allow("203.0.113.7", "admin")
	if !decision.Allowed || decision.Delay != 2*time.Second {
		t.Fatalf("decision=%+v", decision)
	}
	limiter.Success("admin")
	if delay := limiter.Allow("203.0.113.8", "admin").Delay; delay != 0 {
		t.Fatalf("successful login did not clear account delay: %s", delay)
	}
}

func TestFailureDelaySchedule(t *testing.T) {
	tests := []struct {
		failures int
		want     time.Duration
	}{{4, 0}, {5, 250 * time.Millisecond}, {6, 500 * time.Millisecond}, {7, time.Second}, {8, 2 * time.Second}, {1000, 2 * time.Second}}
	for _, test := range tests {
		if got := failureDelay(test.failures); got != test.want {
			t.Fatalf("failures=%d delay=%s want=%s", test.failures, got, test.want)
		}
	}
}

func TestLimiterTTL(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cfg := DefaultConfig()
	limiter := NewWithClock(cfg, func() time.Time { return now })
	limiter.Allow("192.0.2.1", "admin")
	now = now.Add(cfg.EntryTTL + cfg.CleanupInterval + time.Second)
	limiter.Allow("192.0.2.2", "other")
	ips, accounts := limiter.Counts()
	if ips != 1 || accounts != 1 {
		t.Fatalf("expired entries remain: (%d,%d)", ips, accounts)
	}
}
