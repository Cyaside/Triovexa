package runtimebridge

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/ai"
	"github.com/Cyaside/Triovexa/internal/ai/admission"
	"github.com/Cyaside/Triovexa/internal/ai/modelgateway"
	"github.com/Cyaside/Triovexa/internal/coderepair/agent"
)

// This narrow test-only record acknowledges the first failed smoke. It does not
// confer dispatch permission or replace the existing campaign or ledger rows.
type reviewedProviderSmokeAttempt struct {
	Status         string              `json:"status"`
	Code           string              `json:"code"`
	Profile        string              `json:"profile"`
	CampaignID     string              `json:"campaign_id"`
	Model          string              `json:"model"`
	AttemptID      string              `json:"attempt_id"`
	CampaignBefore admission.Campaign  `json:"campaign_before"`
	CampaignAfter  admission.Campaign  `json:"campaign_after"`
	Runtime        *agent.RuntimeTrace `json:"runtime"`
}

// Call only after a human explicitly authorizes another bounded attempt. The
// acknowledgment must identify the retained failure; an ordinary invocation
// still uses validateProviderSmokeBudget and cannot repeat a paid smoke.
func validateReviewedProviderSmokeAttempt(config modelgateway.BudgetConfig, stored admission.Campaign, priorRepairRequests int64,
	acknowledgedAttemptID string, report reviewedProviderSmokeAttempt, retained modelgateway.Summary) error {
	if err := validateProviderSmokeLimits(config, stored); err != nil {
		return err
	}
	if priorRepairRequests != 1 {
		return admission.ErrUncertain
	}
	if report.AttemptID == "" || strings.TrimSpace(report.AttemptID) != report.AttemptID || acknowledgedAttemptID != report.AttemptID ||
		report.Status != "failed" || report.Code != "CONTEXT_LIMIT" || report.Profile != "final-smoke" ||
		report.CampaignID != stored.ID || report.Model != config.Pricing.Model || report.Runtime == nil {
		return admission.ErrMismatch
	}
	runtime := report.Runtime
	if runtime.EngineID != "deepagents" || runtime.EngineVersion != EngineVersion || runtime.ContractVersion != ContractVersion ||
		runtime.Profile != report.Profile || runtime.ConfigVersion == "" || runtime.Accounting != retained {
		return admission.ErrMismatch
	}
	usage := admission.Usage{Present: retained.UsageStatus == "known", PromptTokens: int64(retained.Usage.PromptTokens),
		CompletionTokens: int64(retained.Usage.CompletionTokens), TotalTokens: int64(retained.Usage.TotalTokens),
		CachedInputTokens: int64(retained.Usage.CachedPromptTokens), ReasoningTokens: int64(retained.Usage.ReasoningTokens)}
	if retained.ModelRequests != 1 || retained.ReservedMicroUSD != 0 || retained.LatencyMS < 0 || !admission.ValidUsage(usage) ||
		usage.PromptTokens <= 0 || retained.InputTokenBound < usage.PromptTokens || retained.InputTokenBound > config.MaxInputTokens ||
		usage.CompletionTokens > int64(config.MaxOutputTokens) {
		return admission.ErrUncertain
	}
	cost, err := admission.Cost(config.Pricing, usage.PromptTokens, usage.CompletionTokens)
	if err != nil || cost != retained.AccountedMicroUSD {
		return admission.ErrMismatch
	}
	before, after := report.CampaignBefore, report.CampaignAfter
	if !sameSmokeCampaign(before, stored) || !sameSmokeCampaign(after, stored) || before.CreatedAt.IsZero() ||
		!before.CreatedAt.Equal(after.CreatedAt) || !after.CreatedAt.Equal(stored.CreatedAt) ||
		before.Requests != 0 || before.SpentMicroUSD != 0 || before.AdmittedInputTokens != 0 || before.ReservedMicroUSD != 0 || before.Blocked ||
		after.Requests != 1 || after.SpentMicroUSD != cost || after.ReservedMicroUSD != 0 || after.Blocked ||
		stored.Requests != after.Requests || stored.SpentMicroUSD != after.SpentMicroUSD {
		return admission.ErrMismatch
	}
	// Migration 010 may have replaced the first request's bound with validated
	// prompt usage. The historical summary and immutable request bound must stay
	// exact; no other input counter or reset is accepted for this single request.
	if (after.AdmittedInputTokens != usage.PromptTokens && after.AdmittedInputTokens != retained.InputTokenBound) ||
		(stored.AdmittedInputTokens != usage.PromptTokens && stored.AdmittedInputTokens != retained.InputTokenBound) {
		return admission.ErrMismatch
	}
	return nil
}

func sameSmokeCampaign(a, b admission.Campaign) bool {
	return a.ID == b.ID && a.Profile == b.Profile && a.Offline == b.Offline && a.MaxSpendMicroUSD == b.MaxSpendMicroUSD &&
		a.MaxInputTokens == b.MaxInputTokens && a.MaxRequests == b.MaxRequests
}

func reviewedSmokeFixture() (modelgateway.BudgetConfig, admission.Campaign, reviewedProviderSmokeAttempt, modelgateway.Summary) {
	before := admission.Campaign{ID: "persistent-smoke", Profile: "final-smoke", MaxSpendMicroUSD: 200000,
		MaxInputTokens: 12000, MaxRequests: 6, CreatedAt: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)}
	config := modelgateway.BudgetConfig{Campaign: before, ConfigVersion: "smoke-v1", MaxInputTokens: 6000, MaxOutputTokens: 1500,
		Pricing: admission.Pricing{Version: "synthetic-v1", Provider: "openai-compatible", Model: "fixture", Verified: true,
			InputBoundVerified: true, BillableOutputBound: true, InputMicroUSDPerMillion: 150000, OutputMicroUSDPerMillion: 500000}}
	summary := modelgateway.Summary{ModelRequests: 1, InputTokenBound: 4019, UsageStatus: "known", AccountedMicroUSD: 314, LatencyMS: 100,
		Usage: ai.CompletionUsage{PromptTokens: 1190, CompletionTokens: 270, TotalTokens: 1460, ReasoningTokens: 230}}
	after := before
	after.Requests, after.SpentMicroUSD, after.AdmittedInputTokens = 1, 314, 4019
	report := reviewedProviderSmokeAttempt{Status: "failed", Code: "CONTEXT_LIMIT", Profile: "final-smoke", CampaignID: before.ID, Model: config.Pricing.Model,
		AttemptID: "retained-attempt", CampaignBefore: before, CampaignAfter: after,
		Runtime: &agent.RuntimeTrace{EngineID: "deepagents", EngineVersion: EngineVersion, ContractVersion: ContractVersion,
			ConfigVersion: config.ConfigVersion, Profile: before.Profile, Accounting: summary}}
	stored := after
	stored.AdmittedInputTokens = int64(summary.Usage.PromptTokens)
	return config, stored, report, summary
}

func TestProviderSmokeReviewedAttemptAcceptsOnlyExplicitKnownFailure(t *testing.T) {
	for _, migrated := range []bool{false, true} {
		t.Run(map[bool]string{false: "original-bound", true: "settled-actual-input"}[migrated], func(t *testing.T) {
			config, stored, report, retained := reviewedSmokeFixture()
			if !migrated {
				stored.AdmittedInputTokens = retained.InputTokenBound
			}
			// Different time zones representing the same instant do not imply a reset.
			stored.CreatedAt = stored.CreatedAt.In(time.FixedZone("fixture", 7*60*60))
			if err := validateReviewedProviderSmokeAttempt(config, stored, 1, report.AttemptID, report, retained); err != nil {
				t.Fatal(err)
			}
			if err := validateProviderSmokeBudget(config, stored, 1); !errors.Is(err, admission.ErrUncertain) {
				t.Fatalf("ordinary smoke was allowed to retry: %v", err)
			}
		})
	}
}

func TestProviderSmokeReviewedAttemptRejectsChangedOrUncertainProof(t *testing.T) {
	for _, scenario := range []struct {
		name string
		edit func(*modelgateway.BudgetConfig, *admission.Campaign, *reviewedProviderSmokeAttempt, *modelgateway.Summary, *int64, *string)
	}{
		{"missing approval", func(_ *modelgateway.BudgetConfig, _ *admission.Campaign, _ *reviewedProviderSmokeAttempt, _ *modelgateway.Summary, _ *int64, ack *string) {
			*ack = ""
		}},
		{"different approved attempt", func(_ *modelgateway.BudgetConfig, _ *admission.Campaign, _ *reviewedProviderSmokeAttempt, _ *modelgateway.Summary, _ *int64, ack *string) {
			*ack = "other"
		}},
		{"missing retained attempt", func(_ *modelgateway.BudgetConfig, _ *admission.Campaign, r *reviewedProviderSmokeAttempt, _ *modelgateway.Summary, _ *int64, _ *string) {
			r.AttemptID = ""
		}},
		{"unknown failure", func(_ *modelgateway.BudgetConfig, _ *admission.Campaign, r *reviewedProviderSmokeAttempt, _ *modelgateway.Summary, _ *int64, _ *string) {
			r.Code = "PROVIDER_DISPATCH_UNCERTAIN"
		}},
		{"successful attempt", func(_ *modelgateway.BudgetConfig, _ *admission.Campaign, r *reviewedProviderSmokeAttempt, _ *modelgateway.Summary, _ *int64, _ *string) {
			r.Status = "passed"
		}},
		{"different campaign", func(_ *modelgateway.BudgetConfig, _ *admission.Campaign, r *reviewedProviderSmokeAttempt, _ *modelgateway.Summary, _ *int64, _ *string) {
			r.CampaignID = "replacement"
		}},
		{"different model", func(_ *modelgateway.BudgetConfig, _ *admission.Campaign, r *reviewedProviderSmokeAttempt, _ *modelgateway.Summary, _ *int64, _ *string) {
			r.Model = "other"
		}},
		{"missing runtime", func(_ *modelgateway.BudgetConfig, _ *admission.Campaign, r *reviewedProviderSmokeAttempt, _ *modelgateway.Summary, _ *int64, _ *string) {
			r.Runtime = nil
		}},
		{"changed receipt summary", func(_ *modelgateway.BudgetConfig, _ *admission.Campaign, _ *reviewedProviderSmokeAttempt, s *modelgateway.Summary, _ *int64, _ *string) {
			s.LatencyMS++
		}},
		{"unknown usage", func(_ *modelgateway.BudgetConfig, _ *admission.Campaign, r *reviewedProviderSmokeAttempt, s *modelgateway.Summary, _ *int64, _ *string) {
			s.UsageStatus = "unknown"
			r.Runtime.Accounting = *s
		}},
		{"invalid usage", func(_ *modelgateway.BudgetConfig, _ *admission.Campaign, r *reviewedProviderSmokeAttempt, s *modelgateway.Summary, _ *int64, _ *string) {
			s.Usage.TotalTokens++
			r.Runtime.Accounting = *s
		}},
		{"zero known input", func(_ *modelgateway.BudgetConfig, _ *admission.Campaign, r *reviewedProviderSmokeAttempt, s *modelgateway.Summary, _ *int64, _ *string) {
			s.Usage = ai.CompletionUsage{}
			r.Runtime.Accounting = *s
		}},
		{"pending response", func(_ *modelgateway.BudgetConfig, _ *admission.Campaign, r *reviewedProviderSmokeAttempt, s *modelgateway.Summary, _ *int64, _ *string) {
			s.ReservedMicroUSD = 1
			r.Runtime.Accounting = *s
		}},
		{"more than one response", func(_ *modelgateway.BudgetConfig, _ *admission.Campaign, r *reviewedProviderSmokeAttempt, s *modelgateway.Summary, _ *int64, _ *string) {
			s.ModelRequests = 2
			r.Runtime.Accounting = *s
		}},
		{"missing prior dispatch", func(_ *modelgateway.BudgetConfig, _ *admission.Campaign, _ *reviewedProviderSmokeAttempt, _ *modelgateway.Summary, p *int64, _ *string) {
			*p = 0
		}},
		{"additional prior dispatch", func(_ *modelgateway.BudgetConfig, _ *admission.Campaign, _ *reviewedProviderSmokeAttempt, _ *modelgateway.Summary, p *int64, _ *string) {
			*p = 2
		}},
		{"held campaign spend", func(_ *modelgateway.BudgetConfig, s *admission.Campaign, _ *reviewedProviderSmokeAttempt, _ *modelgateway.Summary, _ *int64, _ *string) {
			s.ReservedMicroUSD = 1
		}},
		{"blocked campaign", func(_ *modelgateway.BudgetConfig, s *admission.Campaign, _ *reviewedProviderSmokeAttempt, _ *modelgateway.Summary, _ *int64, _ *string) {
			s.Blocked = true
		}},
		{"reset money", func(_ *modelgateway.BudgetConfig, s *admission.Campaign, _ *reviewedProviderSmokeAttempt, _ *modelgateway.Summary, _ *int64, _ *string) {
			s.SpentMicroUSD = 0
		}},
		{"reset requests", func(_ *modelgateway.BudgetConfig, s *admission.Campaign, _ *reviewedProviderSmokeAttempt, _ *modelgateway.Summary, _ *int64, _ *string) {
			s.Requests = 0
		}},
		{"intervening request", func(_ *modelgateway.BudgetConfig, s *admission.Campaign, _ *reviewedProviderSmokeAttempt, _ *modelgateway.Summary, _ *int64, _ *string) {
			s.Requests++
		}},
		{"reset input", func(_ *modelgateway.BudgetConfig, s *admission.Campaign, _ *reviewedProviderSmokeAttempt, _ *modelgateway.Summary, _ *int64, _ *string) {
			s.AdmittedInputTokens = 0
		}},
		{"corrupt input", func(_ *modelgateway.BudgetConfig, s *admission.Campaign, _ *reviewedProviderSmokeAttempt, _ *modelgateway.Summary, _ *int64, _ *string) {
			s.AdmittedInputTokens++
		}},
		{"recreated campaign", func(_ *modelgateway.BudgetConfig, s *admission.Campaign, _ *reviewedProviderSmokeAttempt, _ *modelgateway.Summary, _ *int64, _ *string) {
			s.CreatedAt = s.CreatedAt.Add(time.Second)
		}},
		{"rewritten campaign cap", func(c *modelgateway.BudgetConfig, s *admission.Campaign, _ *reviewedProviderSmokeAttempt, _ *modelgateway.Summary, _ *int64, _ *string) {
			c.Campaign.MaxSpendMicroUSD--
			s.MaxSpendMicroUSD = c.Campaign.MaxSpendMicroUSD
		}},
		{"changed tariff", func(c *modelgateway.BudgetConfig, _ *admission.Campaign, _ *reviewedProviderSmokeAttempt, _ *modelgateway.Summary, _ *int64, _ *string) {
			c.Pricing.FixedRequestMicroUSD++
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			config, stored, report, retained := reviewedSmokeFixture()
			prior, acknowledgment := int64(1), report.AttemptID
			scenario.edit(&config, &stored, &report, &retained, &prior, &acknowledgment)
			if err := validateReviewedProviderSmokeAttempt(config, stored, prior, acknowledgment, report, retained); err == nil {
				t.Fatal("unreviewed or changed attempt was admitted")
			}
		})
	}
}
