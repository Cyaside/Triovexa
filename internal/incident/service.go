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
	actions    ActionGenerator
	workflow   PolicyWorkflow
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

type ActionGenerator interface {
	Generate(context.Context, domain.Incident, domain.TriageResult, []domain.EvidenceItem, []domain.DocumentReference) ([]domain.CandidateAction, error)
}

type PolicyWorkflow interface {
	EvaluateActions(context.Context, domain.Incident, []domain.CandidateAction) (domain.Incident, error)
}

func NewService(
	repository storage.Repository,
	collector ContextCollector,
	retriever KnowledgeRetriever,
	generator TriageGenerator,
	actionGenerator ActionGenerator,
	policyWorkflow PolicyWorkflow,
) *Service {
	return &Service{
		repository: repository,
		collector:  collector,
		retriever:  retriever,
		generator:  generator,
		actions:    actionGenerator,
		workflow:   policyWorkflow,
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
		if _, err := s.runReadOnlyTriage(ctx, incident); err != nil {
			return incident, err
		}

		latest, err := s.repository.GetIncident(ctx, incident.ID)
		if err != nil {
			return domain.Incident{}, fmt.Errorf("reload incident after triage: %w", err)
		}
		incident = latest
	}

	return incident, nil
}

func (s *Service) runReadOnlyTriage(ctx context.Context, incident domain.Incident) (domain.Incident, error) {
	var err error
	incident, err = s.transitionIncidentState(ctx, incident, domain.IncidentStateTriaging)
	if err != nil {
		return domain.Incident{}, fmt.Errorf("move incident to triaging: %w", err)
	}

	collectedAt := s.now()
	evidence, collectErr := s.collector.Collect(ctx, incident)
	if collectErr != nil {
		if auditErr := s.audit(ctx, incident.ID, "context_collection", "partial_failure", map[string]any{
			"error": collectErr.Error(),
		}, collectedAt, s.now()); auditErr != nil {
			return domain.Incident{}, fmt.Errorf("audit context collection failure: %w", auditErr)
		}
		evidence = nil
	} else {
		if err := s.repository.SaveEvidenceItems(ctx, evidence); err != nil {
			return domain.Incident{}, fmt.Errorf("save evidence items: %w", err)
		}
		if err := s.audit(ctx, incident.ID, "context_collection", "completed", map[string]any{
			"evidence_count": len(evidence),
		}, collectedAt, s.now()); err != nil {
			return domain.Incident{}, fmt.Errorf("audit context collection: %w", err)
		}
	}

	retrievedAt := s.now()
	documents, retrievalErr := s.retriever.Retrieve(ctx, incident, evidence)
	if retrievalErr != nil {
		if err := s.audit(ctx, incident.ID, "knowledge_retrieval", "partial_failure", map[string]any{
			"error": retrievalErr.Error(),
		}, retrievedAt, s.now()); err != nil {
			return domain.Incident{}, fmt.Errorf("audit retrieval failure: %w", err)
		}
		documents = nil
	} else {
		if err := s.repository.SaveDocumentReferences(ctx, documents); err != nil {
			return domain.Incident{}, fmt.Errorf("save document references: %w", err)
		}
		if err := s.audit(ctx, incident.ID, "knowledge_retrieval", "completed", map[string]any{
			"document_count": len(documents),
		}, retrievedAt, s.now()); err != nil {
			return domain.Incident{}, fmt.Errorf("audit knowledge retrieval: %w", err)
		}
	}

	triagedAt := s.now()
	result, err := s.generator.Generate(ctx, incident, evidence, documents)
	if err != nil {
		if auditErr := s.audit(ctx, incident.ID, "triage_generation", "failed", map[string]any{
			"error": err.Error(),
		}, triagedAt, s.now()); auditErr != nil {
			return domain.Incident{}, fmt.Errorf("audit triage generation failure: %w", auditErr)
		}
		return domain.Incident{}, fmt.Errorf("generate triage result: %w", err)
	}

	if err := s.repository.SaveTriageResult(ctx, result); err != nil {
		return domain.Incident{}, fmt.Errorf("save triage result: %w", err)
	}

	if err := s.audit(ctx, incident.ID, "triage_generation", "completed", map[string]any{
		"hypotheses_count": len(result.Hypotheses),
		"next_steps_count": len(result.NextSteps),
	}, triagedAt, s.now()); err != nil {
		return domain.Incident{}, fmt.Errorf("audit triage generation: %w", err)
	}

	if s.actions == nil {
		return incident, nil
	}

	startedAt := s.now()
	if err := s.audit(ctx, incident.ID, "action_generation", "started", map[string]any{
		"catalog_mode": "heuristic-constrained",
	}, startedAt, startedAt); err != nil {
		return domain.Incident{}, fmt.Errorf("audit action generation start: %w", err)
	}

	actions, err := s.actions.Generate(ctx, incident, result, evidence, documents)
	if err != nil {
		if auditErr := s.audit(ctx, incident.ID, "action_generation", "failed", map[string]any{
			"error": err.Error(),
		}, startedAt, s.now()); auditErr != nil {
			return domain.Incident{}, fmt.Errorf("audit action generation failure: %w", auditErr)
		}
		return domain.Incident{}, fmt.Errorf("generate candidate actions: %w", err)
	}

	if len(actions) > 0 {
		if err := s.repository.SaveCandidateActions(ctx, actions); err != nil {
			return domain.Incident{}, fmt.Errorf("save candidate actions: %w", err)
		}
	}

	validCount, invalidCount := countCandidateActions(actions)
	if err := s.audit(ctx, incident.ID, "action_generation", "completed", map[string]any{
		"candidate_count": validCount,
		"invalid_count":   invalidCount,
	}, startedAt, s.now()); err != nil {
		return domain.Incident{}, fmt.Errorf("audit action generation completion: %w", err)
	}

	if invalidCount > 0 {
		filteredAt := s.now()
		if err := s.audit(ctx, incident.ID, "action_validation", "completed", map[string]any{
			"invalid_count": invalidCount,
		}, filteredAt, filteredAt); err != nil {
			return domain.Incident{}, fmt.Errorf("audit invalid action filter: %w", err)
		}
	}

	if validCount == 0 {
		return incident, nil
	}

	incident, err = s.transitionIncidentState(ctx, incident, domain.IncidentStateActionProposed)
	if err != nil {
		return domain.Incident{}, fmt.Errorf("move incident to action proposed: %w", err)
	}

	if s.workflow == nil {
		return incident, nil
	}

	incident, err = s.workflow.EvaluateActions(ctx, incident, actions)
	if err != nil {
		return domain.Incident{}, fmt.Errorf("evaluate candidate actions: %w", err)
	}

	return incident, nil
}

func (s *Service) transitionIncidentState(ctx context.Context, incident domain.Incident, next domain.IncidentState) (domain.Incident, error) {
	if incident.State == next {
		return incident, nil
	}

	if !CanTransition(incident.State, next) {
		return domain.Incident{}, fmt.Errorf("invalid incident state transition from %q to %q", incident.State, next)
	}

	if err := s.repository.UpdateIncidentState(ctx, incident.ID, next); err != nil {
		return domain.Incident{}, err
	}

	incident.State = next
	incident.UpdatedAt = s.now()
	return incident, nil
}

func countCandidateActions(actions []domain.CandidateAction) (valid int, invalid int) {
	for _, action := range actions {
		if action.Status == domain.CandidateActionStatusInvalid {
			invalid++
			continue
		}
		valid++
	}

	return valid, invalid
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
