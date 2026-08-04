package main

import (
	"testing"
	"time"
)

func TestMetricsRange(t *testing.T) {
	tests := []struct {
		input    string
		duration time.Duration
		step     string
	}{
		{"30m", 30 * time.Minute, "15"},
		{"1h", time.Hour, "15"},
		{"6h", 6 * time.Hour, "60"},
		{"24h", 24 * time.Hour, "300"},
		{"7d", 7 * 24 * time.Hour, "1800"},
		{"30d", 30 * 24 * time.Hour, "7200"},
		{"unsupported", 24 * time.Hour, "300"},
		{"", 24 * time.Hour, "300"},
	}

	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			duration, step := metricsRange(test.input)
			if duration != test.duration || step != test.step {
				t.Fatalf("metricsRange(%q) = (%s, %s), want (%s, %s)", test.input, duration, step, test.duration, test.step)
			}
		})
	}
}
