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

type stubCompleter struct {
	content string
	err     error
}

func (s stubCompleter) CompleteJSON(_ context.Context, _ []ai.ChatMessage) (string, error) {
	return s.content, s.err
}
