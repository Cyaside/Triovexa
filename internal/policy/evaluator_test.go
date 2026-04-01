package policy

import (
	"testing"

	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/execution"
)

func TestEvaluatorEvaluate(t *testing.T) {
	t.Parallel()

	evaluator := NewEvaluator(execution.DefaultCatalog())

	tests := []struct {
		name        string
		action      domain.CandidateAction
		environment string
		killSwitch  bool
		want        domain.PolicyDecisionType
		wantRule    string
	}{
		{
			name: "low risk action still needs approval",
			action: domain.CandidateAction{
				ID:             "a-1",
				ActionType:     "restart_demo_worker",
				TargetResource: "demo-worker",
			},
			environment: "local",
			want:        domain.PolicyDecisionApprovalRequired,
			wantRule:    "risk/low-requires-approval",
		},
		{
			name: "unknown action denied",
			action: domain.CandidateAction{
				ID:             "a-2",
				ActionType:     "drop_production_database",
				TargetResource: "postgres-prod",
			},
			environment: "production",
			want:        domain.PolicyDecisionDeny,
			wantRule:    "catalog/unknown-action",
		},
		{
			name: "medium risk denied in mvp",
			action: domain.CandidateAction{
				ID:             "a-3",
				ActionType:     "restart_demo_service",
				TargetResource: "demo-api",
			},
			environment: "staging",
			want:        domain.PolicyDecisionDeny,
			wantRule:    "risk/medium-blocked-in-mvp",
		},
		{
			name: "kill switch denies all action execution paths",
			action: domain.CandidateAction{
				ID:             "a-4",
				ActionType:     "refresh_demo_cache",
				TargetResource: "demo-cache",
			},
			environment: "local",
			killSwitch:  true,
			want:        domain.PolicyDecisionDeny,
			wantRule:    "safety/global-kill-switch",
		},
		{
			name: "target outside allowlist denied",
			action: domain.CandidateAction{
				ID:             "a-5",
				ActionType:     "retry_demo_background_job",
				TargetResource: "random-runner",
			},
			environment: "local",
			want:        domain.PolicyDecisionDeny,
			wantRule:    "scope/target-allowlist",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			decision := evaluator.Evaluate(tt.action, tt.environment, tt.killSwitch)
			if decision.Decision != tt.want {
				t.Fatalf("decision = %q, want %q", decision.Decision, tt.want)
			}

			if decision.PolicyRuleRef != tt.wantRule {
				t.Fatalf("policy rule = %q, want %q", decision.PolicyRuleRef, tt.wantRule)
			}
		})
	}
}
