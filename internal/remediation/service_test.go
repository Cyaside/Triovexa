package remediation

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/execution"
)

func TestHeuristicGeneratorWorkerStallProducesControlledRiskActions(t *testing.T) {
	t.Parallel()

	generator := NewHeuristicGenerator(execution.DefaultCatalog())
	incident := domain.Incident{
		ID:          "inc-1",
		Environment: "staging",
		ServiceName: "checkout-service",
	}

	evidence := []domain.EvidenceItem{
		newEvidence("ev-metric", "metric", "error_rate=0.12 latency_ms=430 queue_backlog=128", map[string]any{
			"queue_backlog": 128,
			"latency_ms":    430,
		}),
		newEvidence("ev-log", "log", "worker stalled while queue backlog kept growing; retry loop is not consuming new jobs", map[string]any{
			"workerHealthy": false,
		}),
	}

	actions, err := generator.Generate(context.Background(), incident, domain.TriageResult{
		Summary: "worker backlog meningkat",
	}, evidence, nil)
	if err != nil {
		t.Fatalf("generate candidate actions: %v", err)
	}

	if len(actions) != 3 {
		t.Fatalf("candidate actions = %d, want %d", len(actions), 3)
	}

	var sawMediumRisk bool
	for _, action := range actions {
		if action.Status != domain.CandidateActionStatusProposed {
			t.Fatalf("action %q status = %q, want %q", action.ActionType, action.Status, domain.CandidateActionStatusProposed)
		}
		if len(action.EvidenceRefs) == 0 {
			t.Fatalf("action %q should contain evidence refs", action.ActionType)
		}
		if action.ActionType == "pause_demo_queue_consumer" {
			sawMediumRisk = true
			if action.RiskLevel != domain.RiskLevelMedium {
				t.Fatalf("pause_demo_queue_consumer risk = %q, want %q", action.RiskLevel, domain.RiskLevelMedium)
			}
			continue
		}
		if action.RiskLevel != domain.RiskLevelLow {
			t.Fatalf("action %q risk = %q, want %q", action.ActionType, action.RiskLevel, domain.RiskLevelLow)
		}
	}

	if !sawMediumRisk {
		t.Fatalf("expected worker stall incident to include a controlled medium-risk candidate action")
	}
}

func TestHeuristicGeneratorMarksDisallowedEnvironmentInvalid(t *testing.T) {
	t.Parallel()

	generator := NewHeuristicGenerator(execution.DefaultCatalog())
	incident := domain.Incident{
		ID:          "inc-2",
		Environment: "production",
		ServiceName: "checkout-service",
	}

	evidence := []domain.EvidenceItem{
		newEvidence("ev-metric", "metric", "error_rate=0.27 latency_ms=1250 queue_backlog=46", map[string]any{
			"queue_backlog": 46,
			"latency_ms":    1250,
		}),
		newEvidence("ev-log", "log", "request timeouts increased shortly after deploy; downstream dependency appears slower than expected", nil),
		newEvidence("ev-deploy", "deploy", "last_deploy=v1.1.0", map[string]any{
			"last_deploy": "v1.1.0",
		}),
	}

	actions, err := generator.Generate(context.Background(), incident, domain.TriageResult{
		Summary: "timeout muncul setelah deploy",
	}, evidence, nil)
	if err != nil {
		t.Fatalf("generate candidate actions: %v", err)
	}

	if len(actions) != 1 {
		t.Fatalf("candidate actions = %d, want %d", len(actions), 1)
	}

	if actions[0].Status != domain.CandidateActionStatusInvalid {
		t.Fatalf("candidate action status = %q, want %q", actions[0].Status, domain.CandidateActionStatusInvalid)
	}
}

func newEvidence(id string, itemType string, snippet string, metadata map[string]any) domain.EvidenceItem {
	body, _ := json.Marshal(metadata)
	return domain.EvidenceItem{
		ID:           id,
		IncidentID:   "incident",
		Type:         itemType,
		Source:       "demo",
		Snippet:      snippet,
		Timestamp:    time.Now().UTC(),
		MetadataJSON: string(body),
	}
}
