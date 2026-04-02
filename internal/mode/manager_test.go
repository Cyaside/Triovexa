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
