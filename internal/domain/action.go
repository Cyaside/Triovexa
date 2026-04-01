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
	CandidateActionStatusAllowed          CandidateActionStatus = "allowed"
	CandidateActionStatusAwaitingApproval CandidateActionStatus = "awaiting_approval"
	CandidateActionStatusDenied           CandidateActionStatus = "denied"
	CandidateActionStatusApproved         CandidateActionStatus = "approved"
	CandidateActionStatusExecuting        CandidateActionStatus = "executing"
	CandidateActionStatusSucceeded        CandidateActionStatus = "succeeded"
	CandidateActionStatusFailed           CandidateActionStatus = "failed"
)

type CandidateAction struct {
	ID             string
	IncidentID     string
	ActionType     string
	TargetResource string
	ParametersJSON string
	RiskLevel      RiskLevel
	Rationale      string
	Status         CandidateActionStatus
	CreatedAt      time.Time
}
