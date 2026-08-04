package main

import "testing"

func TestAllowedSince(t *testing.T) {
	for _, value := range []string{"15 minutes ago", "2026-08-04T12:00:00Z"} {
		if !allowedSince.MatchString(value) {
			t.Fatalf("should allow %s", value)
		}
	}
	for _, value := range []string{"yesterday; reboot", "--boot", ""} {
		if value != "" && allowedSince.MatchString(value) {
			t.Fatalf("should reject %s", value)
		}
	}
}
func TestPriority(t *testing.T) {
	if priorityNumber("warning") != 4 || priorityName(3) != "error" {
		t.Fatal("priority mapping")
	}
}
