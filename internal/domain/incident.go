package domain

import "time"

type IncidentState string

const (
	IncidentStateDetected          IncidentState = "detected"
	IncidentStateTriaging          IncidentState = "triaging"
	IncidentStateActionProposed    IncidentState = "action_proposed"
	IncidentStateAwaitingApproval  IncidentState = "awaiting_approval"
	IncidentStateApproved          IncidentState = "approved"
	IncidentStateExecutingAction   IncidentState = "executing_action"
	IncidentStateVerifyingAction   IncidentState = "verifying_action"
	IncidentStateResolved          IncidentState = "resolved"
	IncidentStateFailedRemediation IncidentState = "failed_remediation"
	IncidentStateRolledBack        IncidentState = "rolled_back"
	IncidentStateEscalated         IncidentState = "escalated"
	IncidentStateClosed            IncidentState = "closed"
)

type Incident struct {
	ID              string
	ExternalAlertID string
	AlertSource     string
	Title           string
	ServiceName     string
	Environment     string
	Severity        string
	State           IncidentState
	CreatedAt       time.Time
	UpdatedAt       time.Time
}
