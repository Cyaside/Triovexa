package runtimebridge

import (
	"testing"

	"github.com/Cyaside/Triovexa/internal/ai"
	"github.com/Cyaside/Triovexa/internal/ai/admission"
	"github.com/Cyaside/Triovexa/internal/ai/modelgateway"
	"github.com/Cyaside/Triovexa/internal/coderepair/agent"
)

// Test-only admission for the explicitly authorized last repair slot. Both
// previous failures and their immutable ledger summaries must be acknowledged.
// The ordinary smoke and its first-reviewed-attempt guard remain unchanged.
func validateRemainingProviderSmoke(config modelgateway.BudgetConfig, stored admission.Campaign, prior int64,
	reports []reviewedProviderSmokeAttempt, retained []modelgateway.Summary) error {
	if err := validateProviderSmokeLimits(config, stored); err != nil {
		return err
	}
	if prior != 3 || stored.Requests != prior || len(reports) != 2 || len(retained) != 2 {
		return admission.ErrUncertain
	}
	first, second := reports[0], reports[1]
	settledFirst := first.CampaignAfter
	settledFirst.AdmittedInputTokens = int64(retained[0].Usage.PromptTokens)
	if err := validateReviewedProviderSmokeAttempt(config, settledFirst, 1, first.AttemptID, first, retained[0]); err != nil {
		return err
	}
	if second.AttemptID == "" || second.AttemptID == first.AttemptID || second.Status != "failed" || second.Code != "NO_PROGRESS" ||
		second.CampaignID != stored.ID || second.Model != config.Pricing.Model || second.Profile != "final-smoke" || second.Runtime == nil {
		return admission.ErrMismatch
	}
	runtime, summary := second.Runtime, retained[1]
	if runtime.EngineID != "deepagents" || runtime.EngineVersion != EngineVersion || runtime.ContractVersion != ContractVersion ||
		runtime.ConfigVersion != config.ConfigVersion || runtime.Profile != second.Profile || runtime.Accounting != summary {
		return admission.ErrMismatch
	}
	usage := admission.Usage{Present: summary.UsageStatus == "known", PromptTokens: int64(summary.Usage.PromptTokens),
		CompletionTokens: int64(summary.Usage.CompletionTokens), TotalTokens: int64(summary.Usage.TotalTokens),
		CachedInputTokens: int64(summary.Usage.CachedPromptTokens), ReasoningTokens: int64(summary.Usage.ReasoningTokens)}
	if summary.ModelRequests != 2 || summary.ReservedMicroUSD != 0 || summary.LatencyMS < 0 || !admission.ValidUsage(usage) ||
		usage.PromptTokens <= 0 || summary.InputTokenBound < usage.PromptTokens || summary.InputTokenBound > 2*config.MaxInputTokens ||
		usage.CompletionTokens > 2*int64(config.MaxOutputTokens) || summary.AccountedMicroUSD <= 0 {
		return admission.ErrUncertain
	}
	// Summed per-request rounded costs may exceed the rounded aggregate by one.
	minimum, err := admission.Cost(config.Pricing, usage.PromptTokens, usage.CompletionTokens)
	if err != nil || summary.AccountedMicroUSD < minimum || summary.AccountedMicroUSD > minimum+1 {
		return admission.ErrMismatch
	}
	before, after := second.CampaignBefore, second.CampaignAfter
	if !sameSmokeCampaign(before, stored) || !sameSmokeCampaign(after, stored) ||
		!before.CreatedAt.Equal(stored.CreatedAt) || !after.CreatedAt.Equal(stored.CreatedAt) ||
		!before.CreatedAt.Equal(settledFirst.CreatedAt) || before.Requests != settledFirst.Requests ||
		before.SpentMicroUSD != settledFirst.SpentMicroUSD || before.AdmittedInputTokens != settledFirst.AdmittedInputTokens ||
		before.Blocked || before.ReservedMicroUSD != 0 || after.Blocked || after.ReservedMicroUSD != 0 ||
		after.Requests != 3 || after.SpentMicroUSD != before.SpentMicroUSD+summary.AccountedMicroUSD ||
		after.AdmittedInputTokens != before.AdmittedInputTokens+usage.PromptTokens ||
		stored.Requests != after.Requests || stored.SpentMicroUSD != after.SpentMicroUSD || stored.AdmittedInputTokens != after.AdmittedInputTokens {
		return admission.ErrMismatch
	}
	return nil
}

func TestProviderSmokeRemainingSlotRequiresBothFailuresAndUnchangedAllocation(t *testing.T) {
	config, _, first, summary := reviewedSmokeFixture()
	before := first.CampaignAfter
	before.AdmittedInputTokens = int64(summary.Usage.PromptTokens)
	other := modelgateway.Summary{ModelRequests: 2, InputTokenBound: 9854, UsageStatus: "known", AccountedMicroUSD: 565, LatencyMS: 100,
		Usage: ai.CompletionUsage{PromptTokens: 2722, CompletionTokens: 312, TotalTokens: 3034, CachedPromptTokens: 1152, ReasoningTokens: 147}}
	after := before
	after.Requests, after.SpentMicroUSD, after.AdmittedInputTokens = 3, 879, 3912
	second := reviewedProviderSmokeAttempt{Status: "failed", Code: "NO_PROGRESS", Profile: "final-smoke", CampaignID: after.ID, Model: config.Pricing.Model,
		AttemptID: "second-attempt", CampaignBefore: before, CampaignAfter: after,
		Runtime: &agent.RuntimeTrace{EngineID: "deepagents", EngineVersion: EngineVersion, ContractVersion: ContractVersion,
			ConfigVersion: config.ConfigVersion, Profile: "final-smoke", Accounting: other}}
	for _, scenario := range []struct {
		name  string
		edit  func(*admission.Campaign, *[]reviewedProviderSmokeAttempt, *[]modelgateway.Summary)
		valid bool
	}{
		{"last repair slot", func(*admission.Campaign, *[]reviewedProviderSmokeAttempt, *[]modelgateway.Summary) {}, true},
		{"missing failure", func(_ *admission.Campaign, r *[]reviewedProviderSmokeAttempt, _ *[]modelgateway.Summary) {
			*r = (*r)[:1]
		}, false},
		{"intervening triage", func(c *admission.Campaign, _ *[]reviewedProviderSmokeAttempt, _ *[]modelgateway.Summary) {
			c.Requests++
		}, false},
		{"reset spend", func(c *admission.Campaign, _ *[]reviewedProviderSmokeAttempt, _ *[]modelgateway.Summary) {
			c.SpentMicroUSD = 0
		}, false},
		{"changed input", func(c *admission.Campaign, _ *[]reviewedProviderSmokeAttempt, _ *[]modelgateway.Summary) {
			c.AdmittedInputTokens++
		}, false},
		{"unacknowledged failure", func(_ *admission.Campaign, r *[]reviewedProviderSmokeAttempt, _ *[]modelgateway.Summary) {
			(*r)[1].Code = "OTHER"
		}, false},
		{"different attempt", func(_ *admission.Campaign, r *[]reviewedProviderSmokeAttempt, _ *[]modelgateway.Summary) {
			(*r)[1].AttemptID = first.AttemptID
		}, false},
		{"unknown receipt", func(_ *admission.Campaign, _ *[]reviewedProviderSmokeAttempt, s *[]modelgateway.Summary) {
			(*s)[1].UsageStatus = "unknown"
		}, false},
		{"raised quota", func(c *admission.Campaign, _ *[]reviewedProviderSmokeAttempt, _ *[]modelgateway.Summary) {
			c.MaxRequests++
		}, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			stored, reports, summaries := after, []reviewedProviderSmokeAttempt{first, second}, []modelgateway.Summary{summary, other}
			scenario.edit(&stored, &reports, &summaries)
			if err := validateRemainingProviderSmoke(config, stored, 3, reports, summaries); (err == nil) != scenario.valid {
				t.Fatalf("remaining allocation: %v", err)
			}
		})
	}
}
