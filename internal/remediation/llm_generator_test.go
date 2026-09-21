package remediation

import (
	"context"
	"testing"

	"github.com/Cyaside/Triovexa/internal/ai"
	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/execution"
)

func TestLLMGeneratorBuildsCatalogConstrainedActions(t *testing.T) {
	generator := NewLLMGenerator(execution.DefaultCatalog(), remediationStubCompleter{
		content: `{"actions":[{"action_type":"refresh_demo_cache","target_resource":"demo-cache","parameters":{"cache_key":"checkout-session"},"rationale":"aman","evidence_refs":["ev-1"]}]}`,
	})

	actions, err := generator.Generate(context.Background(), domain.Incident{
		ID:          "inc-1",
		Environment: "staging",
	}, domain.TriageResult{
		Summary:     "summary",
		BlastRadius: "blast radius",
	}, []domain.EvidenceItem{{ID: "ev-1"}}, nil)
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("len(actions) = %d, want 1", len(actions))
	}
	if actions[0].ActionType != "refresh_demo_cache" {
		t.Fatalf("ActionType = %q", actions[0].ActionType)
	}
}

type remediationStubCompleter struct {
	content string
	err     error
}

func (s remediationStubCompleter) CompleteJSON(_ context.Context, _ []ai.ChatMessage) (string, error) {
	return s.content, s.err
}
