package policy

import (
	"fmt"
	"slices"
	"time"

	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/execution"
)

type Evaluator struct {
	catalog execution.Catalog
}

func NewEvaluator(catalog execution.Catalog) Evaluator {
	return Evaluator{catalog: catalog}
}

func (e Evaluator) Evaluate(action domain.CandidateAction, environment string, killSwitchEnabled bool) domain.PolicyDecision {
	decision := domain.PolicyDecision{
		CandidateActionID: action.ID,
		DecidedAt:         time.Now().UTC(),
	}

	definition, ok := e.catalog.Get(action.ActionType)
	if !ok {
		decision.Decision = domain.PolicyDecisionDeny
		decision.Reason = "candidate action is not part of the allowlisted catalog"
		decision.PolicyRuleRef = "catalog/unknown-action"
		return decision
	}

	if killSwitchEnabled {
		decision.Decision = domain.PolicyDecisionDeny
		decision.Reason = "global kill switch is enabled"
		decision.PolicyRuleRef = "safety/global-kill-switch"
		return decision
	}

	if !slices.Contains(definition.AllowedEnvironments, environment) {
		decision.Decision = domain.PolicyDecisionDeny
		decision.Reason = fmt.Sprintf("action %q is not allowed in environment %q", definition.Key, environment)
		decision.PolicyRuleRef = "scope/environment-allowlist"
		return decision
	}

	if len(definition.AllowedTargets) > 0 && !slices.Contains(definition.AllowedTargets, action.TargetResource) {
		decision.Decision = domain.PolicyDecisionDeny
		decision.Reason = fmt.Sprintf("target %q is not allowed for action %q", action.TargetResource, definition.Key)
		decision.PolicyRuleRef = "scope/target-allowlist"
		return decision
	}

	switch definition.RiskLevel {
	case domain.RiskLevelLow:
		decision.Decision = domain.PolicyDecisionApprovalRequired
		decision.Reason = "low-risk action is allowlisted but still approval-gated in the initial phases"
		decision.ApprovalRequired = true
		decision.PolicyRuleRef = "risk/low-requires-approval"
	case domain.RiskLevelMedium:
		decision.Decision = domain.PolicyDecisionDeny
		decision.Reason = "medium-risk actions are intentionally blocked until later phases"
		decision.PolicyRuleRef = "risk/medium-blocked-in-mvp"
	case domain.RiskLevelHigh:
		decision.Decision = domain.PolicyDecisionDeny
		decision.Reason = "high-risk actions are blocked during the MVP and foundation phases"
		decision.PolicyRuleRef = "risk/high-blocked-in-mvp"
	default:
		decision.Decision = domain.PolicyDecisionDeny
		decision.Reason = "risk classification is unknown"
		decision.PolicyRuleRef = "risk/unknown"
	}

	return decision
}
