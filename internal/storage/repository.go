package storage

import (
	"context"
	"errors"

	"github.com/Cyaside/Triovexa/internal/domain"
)

var ErrNotFound = errors.New("storage: not found")

type Repository interface {
	Close() error
	CreateIncident(context.Context, domain.Incident) error
	UpdateIncidentState(context.Context, string, domain.IncidentState) error
	GetIncident(context.Context, string) (domain.Incident, error)
	ListIncidents(context.Context) ([]domain.Incident, error)
	AddAuditEvent(context.Context, domain.AuditEvent) error
	ListAuditEvents(context.Context, string) ([]domain.AuditEvent, error)
	SaveEvidenceItems(context.Context, []domain.EvidenceItem) error
	ListEvidenceItems(context.Context, string) ([]domain.EvidenceItem, error)
	SaveDocumentReferences(context.Context, []domain.DocumentReference) error
	ListDocumentReferences(context.Context, string) ([]domain.DocumentReference, error)
	SaveTriageResult(context.Context, domain.TriageResult) error
	GetTriageResult(context.Context, string) (domain.TriageResult, error)
	SaveCandidateActions(context.Context, []domain.CandidateAction) error
	ListCandidateActions(context.Context, string) ([]domain.CandidateAction, error)
	GetCandidateAction(context.Context, string) (domain.CandidateAction, error)
	UpdateCandidateActionStatus(context.Context, string, domain.CandidateActionStatus) error
	SavePolicyDecisions(context.Context, []domain.PolicyDecision) error
	GetPolicyDecision(context.Context, string) (domain.PolicyDecision, error)
	ListPolicyDecisions(context.Context, string) ([]domain.PolicyDecision, error)
	CreateApprovalRecord(context.Context, domain.ApprovalRecord) error
	ListApprovalRecords(context.Context, string) ([]domain.ApprovalRecord, error)
	SaveExecutionRecord(context.Context, domain.ExecutionRecord) error
	ListExecutionRecords(context.Context, string) ([]domain.ExecutionRecord, error)
	ListExecutionRecordsByAction(context.Context, string) ([]domain.ExecutionRecord, error)
}
