package triage

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Cyaside/Triovexa/internal/ai"
	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/mode"
)

func TestLLMGeneratorParsesJSONPayload(t *testing.T) {
	generator := NewLLMGenerator(stubCompleter{
		content: `{"summary":"Summary","hypotheses":["H1"],"blast_radius":"BR","next_steps":["N1"],"draft_status_update":"DSU","confidence_notes":"high"}`,
	})

	result, err := generator.Generate(context.Background(), domain.Incident{ID: "inc-1", Severity: "critical"}, nil, nil)
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if result.Summary != "Summary" {
		t.Fatalf("Summary = %q", result.Summary)
	}
	if result.DraftStatusUpdate != "DSU" {
		t.Fatalf("DraftStatusUpdate = %q", result.DraftStatusUpdate)
	}
}

type recordingGenerator struct {
	calls int
	err   error
}

func (g *recordingGenerator) Generate(context.Context, domain.Incident, []domain.EvidenceItem, []domain.DocumentReference) (domain.TriageResult, error) {
	g.calls++
	return domain.TriageResult{Summary: "fixture"}, g.err
}

func TestLLMFailureDoesNotInvokeDevelopmentHeuristic(t *testing.T) {
	providerErr := errors.New("fixture provider unavailable")
	heuristic, llm := &recordingGenerator{}, &recordingGenerator{err: providerErr}
	modes := mode.NewManager("llm", "demo")
	generator := NewSwitchingGenerator(modes, heuristic, llm)
	_, err := generator.Generate(t.Context(), domain.Incident{}, nil, nil)
	if !errors.Is(err, providerErr) || heuristic.calls != 0 || llm.calls != 1 {
		t.Fatalf("provider failure replaced: err=%v heuristic=%d llm=%d", err, heuristic.calls, llm.calls)
	}
	modes.SetReasoning("heuristic")
	if _, err := generator.Generate(t.Context(), domain.Incident{}, nil, nil); err != nil || heuristic.calls != 1 {
		t.Fatalf("explicit dev mode unavailable: err=%v calls=%d", err, heuristic.calls)
	}
	if _, err := NewSwitchingGenerator(mode.NewManager("llm", "demo"), heuristic, nil).Generate(t.Context(), domain.Incident{}, nil, nil); err == nil || heuristic.calls != 1 {
		t.Fatal("missing llm invoked heuristic")
	}
}

func TestTriagePromptIncludesRunbookContentAndEvidenceIdentity(t *testing.T) {
	prompt := buildTriagePrompt(domain.Incident{}, []domain.EvidenceItem{{ID: "evidence-1", Snippet: "worker heartbeat missing"}}, []domain.DocumentReference{{ID: "book-1", DocumentTitle: "Recovery", Snippet: "Check the consumer heartbeat before restarting."}})
	for _, want := range []string{"Check the consumer heartbeat", `"id":"book-1"`, `"id":"evidence-1"`, `"passage_sha256"`, `"untrusted":true`} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt lacks %s", want)
		}
	}
}

func TestLLMGeneratorCoercesStructuredLists(t *testing.T) {
	generator := NewLLMGenerator(stubCompleter{
		content: `{"summary":"Summary","hypotheses":[{"text":"H1"},{"summary":"H2"}],"blast_radius":"BR","next_steps":[{"value":"N1"},{"content":"N2"}],"confidence_notes":"high"}`,
	})

	result, err := generator.Generate(context.Background(), domain.Incident{ID: "inc-2", Severity: "critical"}, nil, nil)
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if len(result.Hypotheses) != 2 {
		t.Fatalf("len(Hypotheses) = %d, want 2", len(result.Hypotheses))
	}
	if len(result.NextSteps) != 2 {
		t.Fatalf("len(NextSteps) = %d, want 2", len(result.NextSteps))
	}
}

type stubCompleter struct {
	content string
	err     error
}

func (s stubCompleter) CompleteJSON(_ context.Context, _ []ai.ChatMessage) (string, error) {
	return s.content, s.err
}
