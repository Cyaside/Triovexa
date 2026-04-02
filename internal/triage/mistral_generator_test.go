package triage

import (
	"context"
	"testing"

	"github.com/Cyaside/Triovexa/internal/ai"
	"github.com/Cyaside/Triovexa/internal/domain"
)

func TestMistralGeneratorParsesJSONPayload(t *testing.T) {
	generator := NewMistralGenerator(stubCompleter{
		content: `{"summary":"Ringkasan","hypotheses":["H1"],"blast_radius":"BR","next_steps":["N1"],"draft_status_update":"DSU","confidence_notes":"tinggi"}`,
	})

	result, err := generator.Generate(context.Background(), domain.Incident{ID: "inc-1", Severity: "critical"}, nil, nil)
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if result.Summary != "Ringkasan" {
		t.Fatalf("Summary = %q", result.Summary)
	}
	if result.DraftStatusUpdate != "DSU" {
		t.Fatalf("DraftStatusUpdate = %q", result.DraftStatusUpdate)
	}
}

func TestMistralGeneratorCoercesStructuredLists(t *testing.T) {
	generator := NewMistralGenerator(stubCompleter{
		content: `{"summary":"Ringkasan","hypotheses":[{"text":"H1"},{"summary":"H2"}],"blast_radius":"BR","next_steps":[{"value":"N1"},{"content":"N2"}],"confidence_notes":"tinggi"}`,
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
