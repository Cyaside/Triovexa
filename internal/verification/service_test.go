package verification

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Cyaside/Triovexa/internal/demo"
	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/storage"
)

type stubFetcher struct {
	snapshot demo.Snapshot
	err      error
}

func (s stubFetcher) Snapshot(context.Context) (demo.Snapshot, error) {
	return s.snapshot, s.err
}

func TestServiceVerifyExecutionSuccess(t *testing.T) {
	t.Parallel()

	repository := storage.NewMemoryStore()
	incidentRecord, action, executionRecord := seedVerificationFixture(t, repository)

	service := NewService(repository, stubFetcher{snapshot: demo.Snapshot{
		Mode:           demo.ModeHealthy,
		ErrorRate:      0.02,
		LatencyMs:      180,
		QueueBacklog:   5,
		WorkerHealthy:  true,
		LastDeploy:     "v1.2.3",
		LastUpdatedUTC: time.Now().UTC(),
	}})

	result, err := service.VerifyExecution(context.Background(), action, executionRecord)
	if err != nil {
		t.Fatalf("verify execution: %v", err)
	}

	if result.Status != StatusSuccess {
		t.Fatalf("verification status = %q, want %q", result.Status, StatusSuccess)
	}

	updatedIncident, err := repository.GetIncident(context.Background(), incidentRecord.ID)
	if err != nil {
		t.Fatalf("get incident: %v", err)
	}
	if updatedIncident.State != domain.IncidentStateResolved {
		t.Fatalf("incident state = %q, want %q", updatedIncident.State, domain.IncidentStateResolved)
	}
}

func TestServiceVerifyExecutionFailedEscalatesIncident(t *testing.T) {
	t.Parallel()

	repository := storage.NewMemoryStore()
	incidentRecord, action, executionRecord := seedVerificationFixture(t, repository)

	service := NewService(repository, stubFetcher{snapshot: demo.Snapshot{
		Mode:           demo.ModeWorkerStall,
		ErrorRate:      0.19,
		LatencyMs:      750,
		QueueBacklog:   128,
		WorkerHealthy:  false,
		LastDeploy:     "v1.2.3",
		LastUpdatedUTC: time.Now().UTC(),
	}})

	result, err := service.VerifyExecution(context.Background(), action, executionRecord)
	if err != nil {
		t.Fatalf("verify execution: %v", err)
	}

	if result.Status != StatusFailed {
		t.Fatalf("verification status = %q, want %q", result.Status, StatusFailed)
	}

	updatedIncident, err := repository.GetIncident(context.Background(), incidentRecord.ID)
	if err != nil {
		t.Fatalf("get incident: %v", err)
	}
	if updatedIncident.State != domain.IncidentStateEscalated {
		t.Fatalf("incident state = %q, want %q", updatedIncident.State, domain.IncidentStateEscalated)
	}

	updatedAction, err := repository.GetCandidateAction(context.Background(), action.ID)
	if err != nil {
		t.Fatalf("get candidate action: %v", err)
	}
	if updatedAction.Status != domain.CandidateActionStatusFailed {
		t.Fatalf("action status = %q, want %q", updatedAction.Status, domain.CandidateActionStatusFailed)
	}
}

func TestServiceVerifyExecutionInconclusiveEscalatesIncident(t *testing.T) {
	t.Parallel()

	repository := storage.NewMemoryStore()
	incidentRecord, action, executionRecord := seedVerificationFixture(t, repository)

	service := NewService(repository, stubFetcher{snapshot: demo.Snapshot{
		Mode:           demo.ModeHealthy,
		ErrorRate:      0.10,
		LatencyMs:      510,
		QueueBacklog:   60,
		WorkerHealthy:  true,
		LastDeploy:     "v1.2.3",
		LastUpdatedUTC: time.Now().UTC(),
	}})

	result, err := service.VerifyExecution(context.Background(), action, executionRecord)
	if err != nil {
		t.Fatalf("verify execution: %v", err)
	}

	if result.Status != StatusInconclusive {
		t.Fatalf("verification status = %q, want %q", result.Status, StatusInconclusive)
	}

	updatedIncident, err := repository.GetIncident(context.Background(), incidentRecord.ID)
	if err != nil {
		t.Fatalf("get incident: %v", err)
	}
	if updatedIncident.State != domain.IncidentStateEscalated {
		t.Fatalf("incident state = %q, want %q", updatedIncident.State, domain.IncidentStateEscalated)
	}
}

func seedVerificationFixture(t *testing.T, repository storage.Repository) (domain.Incident, domain.CandidateAction, domain.ExecutionRecord) {
	t.Helper()

	incidentRecord := domain.Incident{
		ID:          "incident-verify-1",
		Title:       "checkout timeout after deploy",
		ServiceName: "checkout-service",
		Environment: "staging",
		State:       domain.IncidentStateVerifyingAction,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	if err := repository.CreateIncident(context.Background(), incidentRecord); err != nil {
		t.Fatalf("create incident: %v", err)
	}

	action := domain.CandidateAction{
		ID:             uuid.NewString(),
		IncidentID:     incidentRecord.ID,
		ActionType:     "refresh_demo_cache",
		TargetResource: "checkout-cache",
		ParametersJSON: `{"cache_key":"all"}`,
		RiskLevel:      domain.RiskLevelLow,
		Rationale:      "refreshing cache is safe",
		Status:         domain.CandidateActionStatusSucceeded,
		CreatedAt:      time.Now().UTC(),
	}
	if err := repository.SaveCandidateActions(context.Background(), []domain.CandidateAction{action}); err != nil {
		t.Fatalf("save candidate action: %v", err)
	}

	evidenceMetadata, err := json.Marshal(map[string]any{
		"mode":          string(demo.ModeTimeoutAfterDeploy),
		"error_rate":    0.27,
		"latency_ms":    1200,
		"queue_backlog": 46,
	})
	if err != nil {
		t.Fatalf("marshal evidence metadata: %v", err)
	}
	healthMetadata, err := json.Marshal(map[string]any{
		"mode":          string(demo.ModeTimeoutAfterDeploy),
		"workerHealthy": true,
	})
	if err != nil {
		t.Fatalf("marshal health metadata: %v", err)
	}

	if err := repository.SaveEvidenceItems(context.Background(), []domain.EvidenceItem{
		{
			ID:           uuid.NewString(),
			IncidentID:   incidentRecord.ID,
			Type:         "metric",
			Source:       "demo-service",
			Snippet:      "error_rate=0.27 latency_ms=1200 queue_backlog=46",
			Timestamp:    time.Now().UTC(),
			MetadataJSON: string(evidenceMetadata),
		},
		{
			ID:           uuid.NewString(),
			IncidentID:   incidentRecord.ID,
			Type:         "log",
			Source:       "demo-service",
			Snippet:      "request timeouts increased shortly after deploy",
			Timestamp:    time.Now().UTC(),
			MetadataJSON: string(healthMetadata),
		},
	}); err != nil {
		t.Fatalf("save evidence items: %v", err)
	}

	executionRecord := domain.ExecutionRecord{
		ID:                uuid.NewString(),
		CandidateActionID: action.ID,
		IdempotencyKey:    "execute:" + action.ID,
		InitiatedBy:       "operator-a",
		ExecutorType:      "fake-adapter",
		Status:            "succeeded",
		StartedAt:         time.Now().UTC(),
		FinishedAt:        time.Now().UTC(),
		ResultJSON:        `{"applied":true}`,
	}
	if err := repository.SaveExecutionRecord(context.Background(), executionRecord); err != nil {
		t.Fatalf("save execution record: %v", err)
	}

	return incidentRecord, action, executionRecord
}
