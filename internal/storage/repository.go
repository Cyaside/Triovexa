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
}
