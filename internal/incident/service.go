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
	now        func() time.Time
}

func NewService(repository storage.Repository) *Service {
	return &Service{
		repository: repository,
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

	return incident, nil
}
