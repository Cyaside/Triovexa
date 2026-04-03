package incident

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/storage"
)

type stubCollector struct {
	evidence []domain.EvidenceItem
	err      error
}

func (s stubCollector) Collect(context.Context, domain.Incident) ([]domain.EvidenceItem, error) {
	return s.evidence, s.err
}

type stubRetriever struct {
	documents []domain.DocumentReference
	err       error
}

func (s stubRetriever) Retrieve(context.Context, domain.Incident, []domain.EvidenceItem) ([]domain.DocumentReference, error) {
	return s.documents, s.err
}

type stubGenerator struct {
	result domain.TriageResult
	err    error
}

func (s stubGenerator) Generate(context.Context, domain.Incident, []domain.EvidenceItem, []domain.DocumentReference) (domain.TriageResult, error) {
	return s.result, s.err
}

type stubActionGenerator struct {
	actions []domain.CandidateAction
	err     error
}

func (s stubActionGenerator) Generate(context.Context, domain.Incident, domain.TriageResult, []domain.EvidenceItem, []domain.DocumentReference) ([]domain.CandidateAction, error) {
	return s.actions, s.err
}

func TestServiceRunReadOnlyTriageEscalatesWhenNoValidActionsRemain(t *testing.T) {
	t.Parallel()

	repository := storage.NewMemoryStore()
	now := time.Now().UTC()
	incidentRecord := domain.Incident{
		ID:              uuid.NewString(),
		ExternalAlertID: "alert-empty-actions-001",
		AlertSource:     "grafana",
		Title:           "checkout timeout",
		ServiceName:     "checkout-service",
		Environment:     "staging",
		Severity:        "critical",
		State:           domain.IncidentStateDetected,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := repository.CreateIncident(context.Background(), incidentRecord); err != nil {
		t.Fatalf("create incident: %v", err)
	}

	service := NewService(
		repository,
		stubCollector{},
		stubRetriever{},
		stubGenerator{
			result: domain.TriageResult{
				IncidentID: incidentRecord.ID,
				Summary:    "timeout after deploy",
				Hypotheses: []string{"deployment regression"},
				NextSteps:  []string{"inspect cache layer"},
				CreatedAt:  now,
			},
		},
		stubActionGenerator{
			actions: []domain.CandidateAction{
				{
					ID:         uuid.NewString(),
					IncidentID: incidentRecord.ID,
					ActionType: "refresh_demo_cache",
					Status:     domain.CandidateActionStatusInvalid,
					CreatedAt:  now,
				},
			},
		},
		nil,
	)

	updatedIncident, err := service.runReadOnlyTriage(context.Background(), incidentRecord)
	if err != nil {
		t.Fatalf("runReadOnlyTriage() error = %v", err)
	}

	if updatedIncident.State != domain.IncidentStateEscalated {
		t.Fatalf("incident state = %q, want %q", updatedIncident.State, domain.IncidentStateEscalated)
	}

	storedIncident, err := repository.GetIncident(context.Background(), incidentRecord.ID)
	if err != nil {
		t.Fatalf("GetIncident() error = %v", err)
	}
	if storedIncident.State != domain.IncidentStateEscalated {
		t.Fatalf("stored incident state = %q, want %q", storedIncident.State, domain.IncidentStateEscalated)
	}

	auditTrail, err := repository.ListAuditEvents(context.Background(), incidentRecord.ID)
	if err != nil {
		t.Fatalf("ListAuditEvents() error = %v", err)
	}

	foundEscalationAudit := false
	for _, event := range auditTrail {
		if event.StepName == "action_follow_up" && event.Status == "escalated" {
			foundEscalationAudit = true
			break
		}
	}
	if !foundEscalationAudit {
		t.Fatalf("expected escalation audit event when no valid actions are generated")
	}
}
