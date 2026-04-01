package policy

import (
	"fmt"
	"slices"
	"time"

	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/execution"
	"github.com/Cyaside/Triovexa/internal/topology"
)

type Evaluator struct {
	catalog  execution.Catalog
	topology topology.Registry
}

func NewEvaluator(catalog execution.Catalog) Evaluator {
	return Evaluator{
		catalog:  catalog,
		topology: topology.DefaultRegistry(),
	}
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

	if definition.RiskLevel == domain.RiskLevelMedium {
		targetMetadata, ok := e.topology.Get(action.TargetResource)
		if !ok {
			decision.Decision = domain.PolicyDecisionDeny
			decision.Reason = fmt.Sprintf("medium-risk action %q requires dependency metadata for target %q", definition.Key, action.TargetResource)
			decision.PolicyRuleRef = "dependency/metadata-required"
			return decision
		}
		if len(targetMetadata.Dependencies) == 0 {
			decision.Decision = domain.PolicyDecisionDeny
			decision.Reason = fmt.Sprintf("medium-risk target %q is missing dependency map", action.TargetResource)
			decision.PolicyRuleRef = "dependency/map-required"
			return decision
		}
		if !definition.SupportsRollback || definition.RollbackActionKey == "" {
			decision.Decision = domain.PolicyDecisionDeny
			decision.Reason = fmt.Sprintf("medium-risk action %q must declare a rollback plan before execution is allowed", definition.Key)
			decision.PolicyRuleRef = "safety/rollback-plan-required"
			return decision
		}
		if _, ok := e.catalog.Get(definition.RollbackActionKey); !ok {
			decision.Decision = domain.PolicyDecisionDeny
			decision.Reason = fmt.Sprintf("rollback action %q is missing from catalog", definition.RollbackActionKey)
			decision.PolicyRuleRef = "safety/rollback-action-missing"
			return decision
		}
	}

	switch definition.RiskLevel {
	case domain.RiskLevelLow:
		decision.Decision = domain.PolicyDecisionApprovalRequired
		decision.Reason = "low-risk action is allowlisted but still approval-gated in the initial phases"
		decision.ApprovalRequired = true
		decision.PolicyRuleRef = "risk/low-requires-approval"
	case domain.RiskLevelMedium:
		if !definition.Executable {
			decision.Decision = domain.PolicyDecisionDeny
			decision.Reason = fmt.Sprintf("medium-risk action %q is cataloged but not enabled for execution yet", definition.Key)
			decision.PolicyRuleRef = "risk/medium-disabled"
			return decision
		}

		targetMetadata, _ := e.topology.Get(action.TargetResource)
		decision.Decision = domain.PolicyDecisionApprovalRequired
		decision.Reason = fmt.Sprintf(
			"medium-risk action touches %s owned by %s with dependencies %v; operator approval is mandatory and rollback plan %q is required",
			targetMetadata.Service,
			targetMetadata.Owner,
			targetMetadata.Dependencies,
			definition.RollbackActionKey,
		)
		decision.ApprovalRequired = true
		decision.PolicyRuleRef = "risk/medium-requires-approval"
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
