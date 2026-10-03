package main

import (
	"strings"
	"testing"
)

func TestLiveEvaluatorIsBlockedBeforeCasesOrCredentials(t *testing.T) {
	err := run("missing-case-file", "ignored-report", "provider", "provider", "https://example.invalid/v1", "model", 1)
	if err == nil || !strings.Contains(err.Error(), "shared admission campaign") {
		t.Fatalf("live evaluation not blocked first: %v", err)
	}
}

func TestFixtureEvaluatorRejectsExternalAndDeceptiveHosts(t *testing.T) {
	for _, address := range []string{"https://example.invalid/v1", "http://localhost.example.invalid/v1", "http://127.0.0.1@external.invalid/v1", "http://127.0.0.1/v1?key=example"} {
		err := run("missing-case-file", "ignored-report", "fixture", "fixture", address, "model", 1)
		if err == nil || !strings.Contains(err.Error(), "loopback") {
			t.Fatalf("fixture accepted %q: %v", address, err)
		}
	}
}
