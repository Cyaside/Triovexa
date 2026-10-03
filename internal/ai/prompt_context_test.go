package ai

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Cyaside/Triovexa/internal/domain"
)

func TestDocumentContextIncludesActualBoundedPassageAndProvenance(t *testing.T) {
	items := []domain.DocumentReference{{ID: "runbook-1", DocumentTitle: "Recovery", DocumentType: "runbook", RelevanceReason: "matched title", Snippet: "Check heartbeat, then inspect consumer errors. " + strings.Repeat("中", 1200)}}
	context := DocumentContext(items)
	var passage struct {
		ID, Passage, Freshness string
		SHA                    string `json:"passage_sha256"`
		Truncated, Untrusted   bool
	}
	if err := json.Unmarshal([]byte(context), &passage); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(passage.Passage, "Check heartbeat") || passage.ID != "runbook-1" || len(passage.Passage) > maxContextSnippetBytes || !utf8.ValidString(passage.Passage) || len(passage.SHA) != 64 || !passage.Truncated || !passage.Untrusted || passage.Freshness != "unverified" {
		t.Fatalf("bad document context: %+v", passage)
	}
	if strings.Contains(context, "matched title") {
		t.Fatal("relevance label replaced actual content")
	}
}

func TestEvidenceContextBoundsInputAndDoesNotInventFreshness(t *testing.T) {
	items := make([]domain.EvidenceItem, 100)
	for i := range items {
		items[i] = domain.EvidenceItem{ID: "evidence", Snippet: strings.Repeat("x", 9000)}
	}
	context := EvidenceContext(items)
	if len(context) > maxContextBytes+128 || !strings.Contains(context, `"observed_at":"unknown"`) || !strings.Contains(context, `"omitted":true`) {
		t.Fatalf("context unbounded or freshness invented: bytes=%d", len(context))
	}
}
