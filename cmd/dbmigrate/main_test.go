package main

import "testing"

func TestQuoteLiteralEscapesPassword(t *testing.T) {
	if got, want := quoteLiteral("ab'cd"), "'ab''cd'"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
