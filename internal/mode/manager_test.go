package mode

import "testing"

func TestManagerDefaultsToFallbackModes(t *testing.T) {
	manager := NewManager("", "")
	snapshot := manager.Snapshot()

	if snapshot.Reasoning != ReasoningHeuristic {
		t.Fatalf("Reasoning = %q, want %q", snapshot.Reasoning, ReasoningHeuristic)
	}
	if snapshot.Observability != ObservabilityDemo {
		t.Fatalf("Observability = %q, want %q", snapshot.Observability, ObservabilityDemo)
	}
}

func TestManagerSupportsModeUpdates(t *testing.T) {
	manager := NewManager("heuristic", "demo")
	manager.SetReasoning("mistral")
	manager.SetObservability("grafana")

	snapshot := manager.Snapshot()
	if snapshot.Reasoning != ReasoningMistral {
		t.Fatalf("Reasoning = %q, want %q", snapshot.Reasoning, ReasoningMistral)
	}
	if snapshot.Observability != ObservabilityGrafana {
		t.Fatalf("Observability = %q, want %q", snapshot.Observability, ObservabilityGrafana)
	}
}

func TestParseReasoningRejectsUnknownModes(t *testing.T) {
	parsed, err := ParseReasoning("mistral")
	if err != nil {
		t.Fatalf("ParseReasoning() error = %v", err)
	}
	if parsed != ReasoningMistral {
		t.Fatalf("ParseReasoning() = %q, want %q", parsed, ReasoningMistral)
	}

	if _, err := ParseReasoning("wat"); err == nil {
		t.Fatalf("ParseReasoning() expected error for unknown mode")
	}
}

func TestParseObservabilityRejectsUnknownModes(t *testing.T) {
	parsed, err := ParseObservability("grafana")
	if err != nil {
		t.Fatalf("ParseObservability() error = %v", err)
	}
	if parsed != ObservabilityGrafana {
		t.Fatalf("ParseObservability() = %q, want %q", parsed, ObservabilityGrafana)
	}

	if _, err := ParseObservability("wat"); err == nil {
		t.Fatalf("ParseObservability() expected error for unknown mode")
	}
}
