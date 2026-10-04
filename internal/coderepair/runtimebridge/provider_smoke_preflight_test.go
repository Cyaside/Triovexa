package runtimebridge

import (
	"errors"
	"testing"

	"github.com/Cyaside/Triovexa/internal/ai/admission"
	"github.com/Cyaside/Triovexa/internal/ai/modelgateway"
)

// Run this before reading a saved credential. The campaign must already exist
// in the shared ledger; the smoke harness cannot create a replacement budget.
func validateProviderSmokeBudget(config modelgateway.BudgetConfig, stored admission.Campaign, priorRepairRequests int64) error {
	if err := config.Validate(); err != nil {
		return err
	}
	c := config.Campaign
	if c.Profile != "final-smoke" || c.Offline || c.ID == "" || c.MaxSpendMicroUSD <= 0 || c.MaxSpendMicroUSD > 200000 ||
		stored.ID != c.ID || stored.Profile != c.Profile || stored.Offline != c.Offline || stored.MaxSpendMicroUSD != c.MaxSpendMicroUSD ||
		stored.MaxInputTokens != c.MaxInputTokens || stored.MaxRequests != c.MaxRequests {
		return admission.ErrMismatch
	}
	if !config.Pricing.Verified {
		return admission.ErrPricingUnknown
	}
	if !config.Pricing.InputBoundVerified || !config.Pricing.BillableOutputBound {
		return admission.ErrBillingUnbounded
	}
	if stored.Blocked || stored.ReservedMicroUSD != 0 || priorRepairRequests != 0 {
		return admission.ErrUncertain
	}
	if stored.Requests < 0 || stored.Requests >= stored.MaxRequests || stored.AdmittedInputTokens < 0 ||
		stored.AdmittedInputTokens >= stored.MaxInputTokens || stored.SpentMicroUSD < 0 || stored.SpentMicroUSD >= stored.MaxSpendMicroUSD {
		return admission.ErrBudgetExceeded
	}
	// Target <= $0.10, while the shared ledger remains the <= $0.20 hard cap.
	// Admission still checks the exact final payload for every request.
	worst, err := admission.Cost(config.Pricing, config.MaxInputTokens, int64(config.MaxOutputTokens))
	if err != nil {
		return err
	}
	remaining := stored.MaxRequests - stored.Requests
	if worst > (100000-stored.SpentMicroUSD)/remaining || worst > (stored.MaxSpendMicroUSD-stored.SpentMicroUSD)/remaining {
		return admission.ErrBudgetExceeded
	}
	return nil
}

func TestProviderSmokePreflightRejectsUnsafeOrRepeatedCampaign(t *testing.T) {
	// These are synthetic admission semantics, never a real provider tariff.
	valid := modelgateway.BudgetConfig{Campaign: admission.Campaign{ID: "persistent-smoke", Profile: "final-smoke", MaxSpendMicroUSD: 200000, MaxInputTokens: 12000, MaxRequests: 6},
		Pricing: admission.Pricing{Version: "synthetic", Provider: "openai-compatible", Model: "fixture", Verified: true, InputBoundVerified: true, BillableOutputBound: true,
			InputMicroUSDPerMillion: 1000000, OutputMicroUSDPerMillion: 1000000}, ConfigVersion: "smoke-v1", MaxInputTokens: 6000, MaxOutputTokens: 1500}
	for _, scenario := range []struct {
		name  string
		edit  func(*modelgateway.BudgetConfig, *admission.Campaign)
		prior int64
		want  error
	}{
		{"verified unchanged", func(*modelgateway.BudgetConfig, *admission.Campaign) {}, 0, nil},
		{"unknown tariff", func(c *modelgateway.BudgetConfig, _ *admission.Campaign) { c.Pricing.Verified = false }, 0, admission.ErrPricingUnknown},
		{"unbounded reasoning", func(c *modelgateway.BudgetConfig, _ *admission.Campaign) { c.Pricing.BillableOutputBound = false }, 0, admission.ErrBillingUnbounded},
		{"unverified input", func(c *modelgateway.BudgetConfig, _ *admission.Campaign) { c.Pricing.InputBoundVerified = false }, 0, admission.ErrBillingUnbounded},
		{"new campaign", func(_ *modelgateway.BudgetConfig, s *admission.Campaign) { s.ID = "replacement" }, 0, admission.ErrMismatch},
		{"raised stored limit", func(_ *modelgateway.BudgetConfig, s *admission.Campaign) { s.MaxSpendMicroUSD++ }, 0, admission.ErrMismatch},
		{"held spend", func(_ *modelgateway.BudgetConfig, s *admission.Campaign) { s.ReservedMicroUSD = 1 }, 0, admission.ErrUncertain},
		{"uncertain campaign", func(_ *modelgateway.BudgetConfig, s *admission.Campaign) { s.Blocked = true }, 0, admission.ErrUncertain},
		{"already attempted repair", func(*modelgateway.BudgetConfig, *admission.Campaign) {}, 1, admission.ErrUncertain},
		{"requests exhausted", func(_ *modelgateway.BudgetConfig, s *admission.Campaign) { s.Requests = s.MaxRequests }, 0, admission.ErrBudgetExceeded},
		{"target exceeded", func(_ *modelgateway.BudgetConfig, s *admission.Campaign) { s.SpentMicroUSD = 99999 }, 0, admission.ErrBudgetExceeded},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			config, stored := valid, valid.Campaign
			scenario.edit(&config, &stored)
			if err := validateProviderSmokeBudget(config, stored, scenario.prior); !errors.Is(err, scenario.want) {
				t.Fatalf("preflight: got=%v want=%v", err, scenario.want)
			}
		})
	}
}
