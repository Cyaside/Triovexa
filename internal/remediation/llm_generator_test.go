package remediation

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/ai"
	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/execution"
	"github.com/Cyaside/Triovexa/internal/mode"
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

type recordingActionGenerator struct {
	calls int
	err   error
}

func (g *recordingActionGenerator) Generate(context.Context, domain.Incident, domain.TriageResult, []domain.EvidenceItem, []domain.DocumentReference) ([]domain.CandidateAction, error) {
	g.calls++
	return nil, g.err
}

func TestLLMFailureDoesNotInvokeDevelopmentHeuristic(t *testing.T) {
	providerErr := errors.New("fixture provider unavailable")
	heuristic, llm := &recordingActionGenerator{}, &recordingActionGenerator{err: providerErr}
	modes := mode.NewManager("llm", "demo")
	generator := NewSwitchingGenerator(modes, heuristic, llm)
	_, err := generator.Generate(t.Context(), domain.Incident{}, domain.TriageResult{}, nil, nil)
	if !errors.Is(err, providerErr) || heuristic.calls != 0 || llm.calls != 1 {
		t.Fatalf("provider failure replaced: err=%v heuristic=%d llm=%d", err, heuristic.calls, llm.calls)
	}
	modes.SetReasoning("heuristic")
	if _, err := generator.Generate(t.Context(), domain.Incident{}, domain.TriageResult{}, nil, nil); err != nil || heuristic.calls != 1 {
		t.Fatalf("explicit dev mode unavailable: err=%v calls=%d", err, heuristic.calls)
	}
	if _, err := NewSwitchingGenerator(mode.NewManager("llm", "demo"), heuristic, nil).Generate(t.Context(), domain.Incident{}, domain.TriageResult{}, nil, nil); err == nil || heuristic.calls != 1 {
		t.Fatal("missing llm invoked heuristic")
	}
}

func TestLLMActionRequiresExplicitListAndAvailableEvidence(t *testing.T) {
	for _, content := range []string{`{}`, `{"actions":null}`, `{"actions":[],"unknown":true}`, `{"actions":[]} {"actions":[]}`} {
		generator := NewLLMGenerator(execution.DefaultCatalog(), remediationStubCompleter{content: content})
		if _, err := generator.Generate(t.Context(), domain.Incident{Environment: "staging"}, domain.TriageResult{}, nil, nil); err == nil {
			t.Fatalf("accepted malformed action list %s", content)
		}
	}
	for _, refs := range []string{`[]`, `["forged"]`, `["ev-1","ev-1"]`} {
		generator := NewLLMGenerator(execution.DefaultCatalog(), remediationStubCompleter{content: `{"actions":[{"action_type":"refresh_demo_cache","target_resource":"demo-cache","parameters":{"cache_key":"example"},"rationale":"fixture","evidence_refs":` + refs + `}]}`})
		actions, err := generator.Generate(t.Context(), domain.Incident{Environment: "staging"}, domain.TriageResult{}, []domain.EvidenceItem{{ID: "ev-1"}}, nil)
		if err != nil || len(actions) != 1 || actions[0].Status != domain.CandidateActionStatusInvalid {
			t.Fatalf("ungrounded action accepted: refs=%s actions=%+v err=%v", refs, actions, err)
		}
	}
}

func TestActionPromptIncludesRunbookContent(t *testing.T) {
	prompt := buildActionPrompt(domain.Incident{}, domain.TriageResult{}, nil, []domain.DocumentReference{{ID: "book-1", DocumentTitle: "Recovery", Snippet: "Restart only when the worker heartbeat is stale."}}, execution.DefaultCatalog())
	if !strings.Contains(prompt, "Restart only when") || !strings.Contains(prompt, `"passage_sha256"`) {
		t.Fatal("action prompt has no actual passage/provenance")
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
