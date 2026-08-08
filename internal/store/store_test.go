package store

import "testing"

func TestValidateRewardCursorTransition(t *testing.T) {
	tests := []struct {
		name          string
		expected      int64
		next          int64
		shouldSucceed bool
	}{
		{name: "normal chunk", expected: 100, next: 116, shouldSucceed: true},
		{name: "maximum adaptive chunk", expected: 100, next: 100 + RewardScanMaxTransitionBlocks, shouldSucceed: true},
		{name: "oversized adaptive chunk", expected: 100, next: 101 + RewardScanMaxTransitionBlocks},
		{name: "no movement", expected: 100, next: 100},
		{name: "invalid baseline", expected: 0, next: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateRewardCursorTransition(test.expected, test.next)
			if test.shouldSucceed && err != nil {
				t.Fatalf("transition %d -> %d: %v", test.expected, test.next, err)
			}
			if !test.shouldSucceed && err == nil {
				t.Fatalf("transition %d -> %d unexpectedly succeeded", test.expected, test.next)
			}
		})
	}
}
