package domain

import "time"

type RiskLevel string

const (
	RiskLevelLow    RiskLevel = "low"
	RiskLevelMedium RiskLevel = "medium"
	RiskLevelHigh   RiskLevel = "high"
)

type CandidateActionStatus string

const (
	CandidateActionStatusProposed         CandidateActionStatus = "proposed"
	CandidateActionStatusInvalid          CandidateActionStatus = "invalid"
	CandidateActionStatusAllowed          CandidateActionStatus = "allowed"
	CandidateActionStatusAwaitingApproval CandidateActionStatus = "awaiting_approval"
	CandidateActionStatusDenied           CandidateActionStatus = "denied"
	CandidateActionStatusApproved         CandidateActionStatus = "approved"
	CandidateActionStatusExecuting        CandidateActionStatus = "executing"
	CandidateActionStatusSucceeded        CandidateActionStatus = "succeeded"
	CandidateActionStatusFailed           CandidateActionStatus = "failed"
	CandidateActionStatusRolledBack       CandidateActionStatus = "rolled_back"
)

type CandidateAction struct {
	ID             string
	IncidentID     string
	ActionType     string
	TargetResource string
	ParametersJSON string
	RiskLevel      RiskLevel
	Rationale      string
	EvidenceRefs   []string
	ApprovalHint   string
	Status         CandidateActionStatus
	CreatedAt      time.Time
}
