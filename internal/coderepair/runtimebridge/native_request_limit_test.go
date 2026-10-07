package runtimebridge

import (
	"context"
	"errors"
	"testing"

	"github.com/Cyaside/Triovexa/internal/ai/admission"
	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/coderepair/agent"
)

func TestNativeModelRequestLimitPreservesProfileAndOnlyReducesAllowance(t *testing.T) {
	for _, profile := range []struct {
		name string
		max  int
	}{{"offline-fixture", 4}, {"final-smoke", 4}, {"internal", 20}} {
		t.Run(profile.name, func(t *testing.T) {
			for ceiling := -1; ceiling <= profile.max+1; ceiling++ {
				got, err := nativeModelRequestLimit(profile.name, ceiling)
				if ceiling < 0 || ceiling > profile.max {
					if err == nil || got != 0 {
						t.Fatalf("invalid ceiling %d widened %s profile: limit=%d error=%v", ceiling, profile.name, got, err)
					}
					continue
				}
				want := ceiling
				if want == 0 {
					want = profile.max
				}
				if err != nil || got != want {
					t.Fatalf("ceiling %d for %s: limit=%d want=%d error=%v", ceiling, profile.name, got, want, err)
				}
			}
		})
	}
	for _, profile := range []string{"", "unlimited", "FINAL-SMOKE"} {
		if got, err := nativeModelRequestLimit(profile, 1); err == nil || got != 0 {
			t.Fatalf("unknown profile %q obtained a model allowance", profile)
		}
	}
}

type requestLimitFixtureStore struct {
	claimFixture
	toolReceiptFixtureStore
}

func TestNativeRunnerRejectsInvalidRequestCeilingBeforeCampaignOrTools(t *testing.T) {
	for _, ceiling := range []int{-1, 5} {
		attempt, _, cipher := pinnedFixture(t)
		ledger, err := admission.NewService(admission.NewMemoryStore())
		if err != nil {
			t.Fatal(err)
		}
		store := &requestLimitFixtureStore{}
		runner := &NativeRunner{Store: store, Ledger: ledger, Cipher: cipher, MaxModelRequests: ceiling}
		claim := agent.ClaimedInvestigation{Attempt: attempt}
		result := runner.Investigate(agent.WithClaimedInvestigation(context.Background(), claim), nil,
			coderepair.RepositoryBinding{}, coderepair.EvidenceSnapshot{},
			coderepair.AgentSelection{Provider: attempt.Provider, Model: attempt.Model, PromptVersion: attempt.PromptVersion}, "go-test-workload")
		if result.Status != coderepair.StateBlocked || result.Code != "INVALID_SCOPE" || store.starts != 0 || store.completions != 0 {
			t.Fatalf("invalid ceiling %d reached investigation effects: status=%s code=%s", ceiling, result.Status, result.Code)
		}
		if _, err := ledger.GetCampaign(context.Background(), attempt.Runtime.CampaignID); !errors.Is(err, admission.ErrNotFound) {
			t.Fatalf("invalid ceiling %d mutated campaign state: %v", ceiling, err)
		}
	}
}
