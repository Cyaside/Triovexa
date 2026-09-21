package incident

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Cyaside/Triovexa/internal/alerting"
	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/storage"
)

type stubCollector struct {
	evidence []domain.EvidenceItem
	err      error
}

type sequenceCollector struct {
	calls int
}

func (s *sequenceCollector) Collect(_ context.Context, incident domain.Incident) ([]domain.EvidenceItem, error) {
	s.calls++
	return []domain.EvidenceItem{{
		ID: incident.ID + "-evidence-" + string(rune('0'+s.calls)), IncidentID: incident.ID,
		Type: "metric", Source: "workload-control", Timestamp: time.Now().UTC(), MetadataJSON: `{"complete":true}`,
	}}, nil
}

func TestSameFingerprintCreatesNewEpisodeAfterTerminalIncident(t *testing.T) {
	repository := storage.NewMemoryStore()
	service := NewService(repository, nil, nil, nil, nil, nil)
	payload := alerting.GrafanaWebhookPayload{
		Title:        "queue worker stalled",
		CommonLabels: map[string]string{"service": "queue-worker", "environment": "staging", "severity": "critical"},
		Alerts: []alerting.GrafanaAlert{{
			Status: "firing", Fingerprint: "worker-stall-episode", StartsAt: time.Now().UTC(),
			Labels:      map[string]string{"service": "queue-worker", "environment": "staging", "severity": "critical"},
			Annotations: map[string]string{"summary": "queue worker stalled"},
		}},
	}
	first, shouldTriage, err := service.acceptGrafanaWebhook(context.Background(), payload, false)
	if err != nil || shouldTriage {
		t.Fatalf("first intake: incident=%#v triage=%t err=%v", first, shouldTriage, err)
	}
	duplicate, shouldTriage, err := service.acceptGrafanaWebhook(context.Background(), payload, false)
	if err != nil || shouldTriage || duplicate.ID != first.ID {
		t.Fatalf("duplicate intake: incident=%#v triage=%t err=%v", duplicate, shouldTriage, err)
	}
	if err := repository.UpdateIncidentState(context.Background(), first.ID, domain.IncidentStateClosed); err != nil {
		t.Fatal(err)
	}
	second, shouldTriage, err := service.acceptGrafanaWebhook(context.Background(), payload, false)
	if err != nil || shouldTriage {
		t.Fatalf("second episode intake: incident=%#v triage=%t err=%v", second, shouldTriage, err)
	}
	if second.ID == first.ID {
		t.Fatal("terminal incident was reused instead of creating a new episode")
	}
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

type capturingActionGenerator struct {
	evidence []domain.EvidenceItem
	action   domain.CandidateAction
}

func (s *capturingActionGenerator) Generate(_ context.Context, _ domain.Incident, _ domain.TriageResult, evidence []domain.EvidenceItem, _ []domain.DocumentReference) ([]domain.CandidateAction, error) {
	s.evidence = append([]domain.EvidenceItem(nil), evidence...)
	return []domain.CandidateAction{s.action}, nil
}

func TestRunReadOnlyTriageRefreshesEvidenceBeforeActionGeneration(t *testing.T) {
	repository := storage.NewMemoryStore()
	now := time.Now().UTC()
	incidentRecord := domain.Incident{
		ID: uuid.NewString(), ExternalAlertID: "refresh-action-evidence", AlertSource: "grafana",
		Title: "worker stall", ServiceName: "queue-worker", Environment: "staging", Severity: "high",
		State: domain.IncidentStateDetected, CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateIncident(context.Background(), incidentRecord); err != nil {
		t.Fatal(err)
	}
	collector := &sequenceCollector{}
	actions := &capturingActionGenerator{action: domain.CandidateAction{
		ID: uuid.NewString(), IncidentID: incidentRecord.ID, ActionType: "restart_worker",
		Status: domain.CandidateActionStatusAwaitingApproval, CreatedAt: now,
	}}
	service := NewService(repository, collector, stubRetriever{}, stubGenerator{result: domain.TriageResult{
		ID: uuid.NewString(), IncidentID: incidentRecord.ID, Summary: "worker stalled", CreatedAt: now,
	}}, actions, nil)

	if _, err := service.runReadOnlyTriage(context.Background(), incidentRecord); err != nil {
		t.Fatalf("runReadOnlyTriage() error = %v", err)
	}
	if collector.calls != 2 {
		t.Fatalf("collector calls = %d, want 2", collector.calls)
	}
	if len(actions.evidence) != 1 || actions.evidence[0].ID != incidentRecord.ID+"-evidence-2" {
		t.Fatalf("action evidence = %#v, want refreshed snapshot", actions.evidence)
	}
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
