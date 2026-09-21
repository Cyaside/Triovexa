package remediation

import (
	"context"
	"strings"
	"testing"
	"time"

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

func TestLLMGeneratorRecordsProviderMetadata(t *testing.T) {
	generator := NewLLMGenerator(execution.DefaultCatalog(), remediationDetailedStubCompleter{
		result: ai.CompletionResult{
			Content:  `{"actions":[{"action_type":"refresh_demo_cache","target_resource":"demo-cache","parameters":{"cache_key":"checkout-session"},"rationale":"Refresh the stale cache.","evidence_refs":["ev-1"]}]}`,
			Provider: "openai-compatible",
			Model:    "glm-5.3-flash",
			Latency:  1250 * time.Millisecond,
			Usage:    ai.CompletionUsage{PromptTokens: 20, CompletionTokens: 10},
		},
	})

	actions, err := generator.Generate(context.Background(), domain.Incident{
		ID:          "inc-1",
		Environment: "staging",
	}, domain.TriageResult{Summary: "summary"}, []domain.EvidenceItem{{ID: "ev-1"}}, nil)
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("len(actions) = %d, want 1", len(actions))
	}
	if !strings.Contains(actions[0].ApprovalHint, "model=glm-5.3-flash") {
		t.Fatalf("ApprovalHint = %q, want provider metadata", actions[0].ApprovalHint)
	}
}

type remediationStubCompleter struct {
	content string
	err     error
}

func (s remediationStubCompleter) CompleteJSON(_ context.Context, _ []ai.ChatMessage) (string, error) {
	return s.content, s.err
}

type remediationDetailedStubCompleter struct {
	result ai.CompletionResult
	err    error
}

func (s remediationDetailedStubCompleter) CompleteJSON(_ context.Context, _ []ai.ChatMessage) (string, error) {
	return s.result.Content, s.err
}

func (s remediationDetailedStubCompleter) CompleteJSONDetailed(_ context.Context, _ []ai.ChatMessage) (ai.CompletionResult, error) {
	return s.result, s.err
}
