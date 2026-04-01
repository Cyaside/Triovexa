package domain

import "time"

type PolicyDecisionType string

const (
	PolicyDecisionAllow            PolicyDecisionType = "allow"
	PolicyDecisionApprovalRequired PolicyDecisionType = "approval_required"
	PolicyDecisionDeny             PolicyDecisionType = "deny"
)

type PolicyDecision struct {
	ID                string
	CandidateActionID string
	Decision          PolicyDecisionType
	Reason            string
	ApprovalRequired  bool
	PolicyRuleRef     string
	DecidedAt         time.Time
}
