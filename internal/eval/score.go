package eval

import (
	"sort"
	"time"

	"github.com/Cyaside/Triovexa/internal/ai"
	"github.com/Cyaside/Triovexa/internal/domain"
)

// Score checks the complete action multiset and reference validity. Citation
// membership is not semantic evidence grounding; that remains unassessed.
func Score(item Case, evidence []domain.EvidenceItem, actions []domain.CandidateAction, repeat int, latency time.Duration) CaseResult {
	expected := append([]string{}, item.ExpectedActions...)
	if item.ExpectedActions == nil && item.ExpectedAction != "" {
		expected = append(expected, item.ExpectedAction)
	}
	sort.Strings(expected)
	result := CaseResult{CaseID: item.ID, Repeat: repeat, ExpectedActions: expected, ProposedActions: []string{}, LatencyMilliseconds: latency.Milliseconds(), GroundingStatus: "not_assessed"}
	remaining := make(map[string]int, len(expected))
	for _, name := range expected {
		remaining[name]++
	}
	validEvidence := make(map[string]bool, len(evidence))
	for _, entry := range evidence {
		if entry.ID != "" && (entry.IncidentID == "" || entry.IncidentID == item.ID) {
			validEvidence[entry.ID] = true
		}
	}
	result.CitationValidityAssessed = len(actions) > 0
	result.CitationsValid = len(actions) > 0
	for _, action := range actions {
		result.ProposedActions = append(result.ProposedActions, action.ActionType)
		if remaining[action.ActionType] > 0 {
			remaining[action.ActionType]--
		} else {
			result.ExtraActions++
		}
		if action.Status == domain.CandidateActionStatusInvalid {
			result.InvalidProposals++
		}
		if len(action.EvidenceRefs) == 0 {
			result.CitationsValid = false
		}
		seen := make(map[string]bool, len(action.EvidenceRefs))
		for _, reference := range action.EvidenceRefs {
			if !validEvidence[reference] || seen[reference] {
				result.CitationsValid = false
			}
			seen[reference] = true
		}
	}
	sort.Strings(result.ProposedActions)
	result.ExpectedActionsPresent = true
	for _, missing := range remaining {
		if missing > 0 {
			result.ExpectedActionsPresent = false
		}
	}
	result.Accurate = result.ExpectedActionsPresent && result.ExtraActions == 0 && result.InvalidProposals == 0 && (!result.CitationValidityAssessed || result.CitationsValid)
	return result
}

func Summarize(results []CaseResult, cases int) Summary {
	summary := Summary{Runs: len(results), Cases: cases}
	var correct, citations int
	var latency int64
	for _, result := range results {
		if result.FailureStage == "" {
			summary.SuccessfulRuns++
		} else {
			summary.FailedRuns++
		}
		if result.Accurate && result.FailureStage == "" {
			correct++
		}
		if result.CitationValidityAssessed {
			summary.CitationValidityRuns++
			if result.CitationsValid {
				citations++
			}
		}
		summary.InvalidProposals += result.InvalidProposals
		summary.ExtraActions += result.ExtraActions
		latency += result.LatencyMilliseconds
		for _, observation := range result.UsageObservations {
			switch observation.Status {
			case ai.UsageKnown:
				summary.UsageKnownCalls++
				summary.KnownUsageTokens.PromptTokens += observation.Usage.PromptTokens
				summary.KnownUsageTokens.CompletionTokens += observation.Usage.CompletionTokens
				summary.KnownUsageTokens.TotalTokens += observation.Usage.TotalTokens
				summary.KnownUsageTokens.CachedPromptTokens += observation.Usage.CachedPromptTokens
				summary.KnownUsageTokens.ReasoningTokens += observation.Usage.ReasoningTokens
			case ai.UsageInvalid:
				summary.UsageInvalidCalls++
			default:
				summary.UsageMissingCalls++
			}
		}
	}
	if len(results) > 0 {
		summary.AccuracyPercent = float64(correct) * 100 / float64(len(results))
		summary.AverageLatencyMS = float64(latency) / float64(len(results))
	}
	if summary.CitationValidityRuns > 0 {
		summary.CitationValidityPercent = float64(citations) * 100 / float64(summary.CitationValidityRuns)
	}
	return summary
}
