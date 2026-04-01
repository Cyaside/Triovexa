package incident

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/Cyaside/Triovexa/internal/alerting"
	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/storage"
)

type Service struct {
	repository storage.Repository
	collector  ContextCollector
	retriever  KnowledgeRetriever
	generator  TriageGenerator
	now        func() time.Time
}

type ContextCollector interface {
	Collect(context.Context, domain.Incident) ([]domain.EvidenceItem, error)
}

type KnowledgeRetriever interface {
	Retrieve(context.Context, domain.Incident, []domain.EvidenceItem) ([]domain.DocumentReference, error)
}

type TriageGenerator interface {
	Generate(context.Context, domain.Incident, []domain.EvidenceItem, []domain.DocumentReference) (domain.TriageResult, error)
}

func NewService(
	repository storage.Repository,
	collector ContextCollector,
	retriever KnowledgeRetriever,
	generator TriageGenerator,
) *Service {
	return &Service{
		repository: repository,
		collector:  collector,
		retriever:  retriever,
		generator:  generator,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
}

func (s *Service) IngestGrafanaWebhook(ctx context.Context, payload alerting.GrafanaWebhookPayload) (domain.Incident, error) {
	normalized, err := alerting.NormalizeGrafanaPayload(payload)
	if err != nil {
		return domain.Incident{}, fmt.Errorf("normalize grafana payload: %w", err)
	}

	now := s.now()
	incident := domain.Incident{
		ID:              uuid.NewString(),
		ExternalAlertID: normalized.ExternalAlertID,
		AlertSource:     normalized.AlertSource,
		Title:           normalized.Title,
		ServiceName:     normalized.ServiceName,
		Environment:     normalized.Environment,
		Severity:        normalized.Severity,
		State:           domain.IncidentStateDetected,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	if err := s.repository.CreateIncident(ctx, incident); err != nil {
		return domain.Incident{}, fmt.Errorf("create incident: %w", err)
	}

	details, err := json.Marshal(map[string]any{
		"alert_source":      normalized.AlertSource,
		"external_alert_id": normalized.ExternalAlertID,
		"service_name":      normalized.ServiceName,
		"environment":       normalized.Environment,
		"severity":          normalized.Severity,
		"labels":            normalized.Labels,
	})
	if err != nil {
		return domain.Incident{}, fmt.Errorf("marshal intake audit details: %w", err)
	}

	auditEvent := domain.AuditEvent{
		ID:          uuid.NewString(),
		IncidentID:  incident.ID,
		StepName:    "webhook_intake",
		Status:      "completed",
		DetailsJSON: string(details),
		StartedAt:   now,
		FinishedAt:  now,
	}

	if err := s.repository.AddAuditEvent(ctx, auditEvent); err != nil {
		return domain.Incident{}, fmt.Errorf("create intake audit event: %w", err)
	}

	if s.collector != nil && s.retriever != nil && s.generator != nil {
		if err := s.runReadOnlyTriage(ctx, incident); err != nil {
			return incident, err
		}
	}

	return incident, nil
}

func (s *Service) runReadOnlyTriage(ctx context.Context, incident domain.Incident) error {
	if err := s.repository.UpdateIncidentState(ctx, incident.ID, domain.IncidentStateTriaging); err != nil {
		return fmt.Errorf("move incident to triaging: %w", err)
	}

	collectedAt := s.now()
	evidence, collectErr := s.collector.Collect(ctx, incident)
	if collectErr != nil {
		if auditErr := s.audit(ctx, incident.ID, "context_collection", "partial_failure", map[string]any{
			"error": collectErr.Error(),
		}, collectedAt, s.now()); auditErr != nil {
			return fmt.Errorf("audit context collection failure: %w", auditErr)
		}
		evidence = nil
	} else {
		if err := s.repository.SaveEvidenceItems(ctx, evidence); err != nil {
			return fmt.Errorf("save evidence items: %w", err)
		}
		if err := s.audit(ctx, incident.ID, "context_collection", "completed", map[string]any{
			"evidence_count": len(evidence),
		}, collectedAt, s.now()); err != nil {
			return fmt.Errorf("audit context collection: %w", err)
		}
	}

	retrievedAt := s.now()
	documents, retrievalErr := s.retriever.Retrieve(ctx, incident, evidence)
	if retrievalErr != nil {
		if err := s.audit(ctx, incident.ID, "knowledge_retrieval", "partial_failure", map[string]any{
			"error": retrievalErr.Error(),
		}, retrievedAt, s.now()); err != nil {
			return fmt.Errorf("audit retrieval failure: %w", err)
		}
		documents = nil
	} else {
		if err := s.repository.SaveDocumentReferences(ctx, documents); err != nil {
			return fmt.Errorf("save document references: %w", err)
		}
		if err := s.audit(ctx, incident.ID, "knowledge_retrieval", "completed", map[string]any{
			"document_count": len(documents),
		}, retrievedAt, s.now()); err != nil {
			return fmt.Errorf("audit knowledge retrieval: %w", err)
		}
	}

	triagedAt := s.now()
	result, err := s.generator.Generate(ctx, incident, evidence, documents)
	if err != nil {
		if auditErr := s.audit(ctx, incident.ID, "triage_generation", "failed", map[string]any{
			"error": err.Error(),
		}, triagedAt, s.now()); auditErr != nil {
			return fmt.Errorf("audit triage generation failure: %w", auditErr)
		}
		return fmt.Errorf("generate triage result: %w", err)
	}

	if err := s.repository.SaveTriageResult(ctx, result); err != nil {
		return fmt.Errorf("save triage result: %w", err)
	}

	if err := s.audit(ctx, incident.ID, "triage_generation", "completed", map[string]any{
		"hypotheses_count": len(result.Hypotheses),
		"next_steps_count": len(result.NextSteps),
	}, triagedAt, s.now()); err != nil {
		return fmt.Errorf("audit triage generation: %w", err)
	}

	return nil
}

func (s *Service) audit(
	ctx context.Context,
	incidentID string,
	stepName string,
	status string,
	details map[string]any,
	startedAt time.Time,
	finishedAt time.Time,
) error {
	body, err := json.Marshal(details)
	if err != nil {
		return err
	}

	return s.repository.AddAuditEvent(ctx, domain.AuditEvent{
		ID:          uuid.NewString(),
		IncidentID:  incidentID,
		StepName:    stepName,
		Status:      status,
		DetailsJSON: string(body),
		StartedAt:   startedAt,
		FinishedAt:  finishedAt,
	})
}
