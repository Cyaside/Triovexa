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
	manager.SetReasoning("llm")
	manager.SetObservability("grafana")

	snapshot := manager.Snapshot()
	if snapshot.Reasoning != ReasoningLLM {
		t.Fatalf("Reasoning = %q, want %q", snapshot.Reasoning, ReasoningLLM)
	}
	if snapshot.Observability != ObservabilityGrafana {
		t.Fatalf("Observability = %q, want %q", snapshot.Observability, ObservabilityGrafana)
	}
}

func TestParseReasoningRejectsUnknownModes(t *testing.T) {
	parsed, err := ParseReasoning("llm")
	if err != nil || parsed != ReasoningLLM {
		t.Fatalf("ParseReasoning() = %q, %v; want %q", parsed, err, ReasoningLLM)
	}

	if _, err := ParseReasoning("legacy-provider"); err == nil {
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
