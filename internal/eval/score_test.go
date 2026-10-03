package eval

import (
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/ai"
	"github.com/Cyaside/Triovexa/internal/domain"
)

func TestExactActionScoreRejectsExtrasDuplicatesInvalidAndForgedCitations(t *testing.T) {
	item := Case{ID: "case-1", ExpectedAction: "restart_worker"}
	evidence := []domain.EvidenceItem{{ID: "ev-1", IncidentID: item.ID}}
	valid := domain.CandidateAction{ActionType: "restart_worker", Status: domain.CandidateActionStatusProposed, EvidenceRefs: []string{"ev-1"}}
	for name, actions := range map[string][]domain.CandidateAction{
		"valid":            {valid},
		"extra":            {valid, {ActionType: "pause_consumer", EvidenceRefs: []string{"ev-1"}}},
		"duplicate":        {valid, valid},
		"invalid":          {{ActionType: "restart_worker", Status: domain.CandidateActionStatusInvalid, EvidenceRefs: []string{"ev-1"}}},
		"forged citation":  {{ActionType: "restart_worker", EvidenceRefs: []string{"forged"}}},
		"missing citation": {{ActionType: "restart_worker"}},
	} {
		t.Run(name, func(t *testing.T) {
			result := Score(item, evidence, actions, 1, time.Millisecond)
			if result.Accurate != (name == "valid") {
				t.Fatalf("result=%+v", result)
			}
			if result.GroundingStatus != "not_assessed" {
				t.Fatal("ID validity misrepresented as grounding")
			}
		})
	}
}

func TestFailureAndAbstentionDenominatorsRemainHonest(t *testing.T) {
	abstention := Score(Case{ID: "ambiguous"}, nil, nil, 1, time.Millisecond)
	if !abstention.Accurate || abstention.CitationValidityAssessed {
		t.Fatalf("abstention inflated citations: %+v", abstention)
	}
	failure := abstention
	failure.FailureStage = "triage"
	failure.Accurate = false
	positive := Score(Case{ID: "positive", ExpectedAction: "restart_worker"}, []domain.EvidenceItem{{ID: "ev"}}, []domain.CandidateAction{{ActionType: "restart_worker", EvidenceRefs: []string{"ev"}}}, 1, time.Millisecond)
	positive.UsageObservations = []UsageObservation{{Status: ai.UsageKnown, Usage: ai.CompletionUsage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8, CachedPromptTokens: 4, ReasoningTokens: 2}}, {Status: ai.UsageMissing, Usage: ai.CompletionUsage{TotalTokens: 100}}}
	summary := Summarize([]CaseResult{abstention, failure, positive}, 3)
	if summary.Runs != 3 || summary.FailedRuns != 1 || summary.CitationValidityRuns != 1 || summary.AccuracyPercent < 66 || summary.AccuracyPercent > 67 || summary.UsageMissingCalls != 1 || summary.KnownUsageTokens.TotalTokens != 8 {
		t.Fatalf("invalid denominators/accounting: %+v", summary)
	}
}
