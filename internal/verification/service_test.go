package verification

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Cyaside/Triovexa/internal/demo"
	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/execution"
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
	}}, execution.DefaultCatalog(), nil)

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
	}}, execution.DefaultCatalog(), nil)

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
	}}, execution.DefaultCatalog(), nil)

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

func TestServiceVerifyExecutionFailedTriggersRollbackWhenAvailable(t *testing.T) {
	t.Parallel()

	repository := storage.NewMemoryStore()
	incidentRecord := domain.Incident{
		ID:          "incident-verify-rollback-1",
		Title:       "checkout consumer backlog",
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
		ActionType:     "pause_demo_queue_consumer",
		TargetResource: "demo-queue-consumer",
		ParametersJSON: `{}`,
		RiskLevel:      domain.RiskLevelMedium,
		Rationale:      "pause consumer to limit blast radius",
		Status:         domain.CandidateActionStatusSucceeded,
		CreatedAt:      time.Now().UTC(),
	}
	if err := repository.SaveCandidateActions(context.Background(), []domain.CandidateAction{action}); err != nil {
		t.Fatalf("save candidate action: %v", err)
	}

	evidenceMetadata, err := json.Marshal(map[string]any{
		"mode":          string(demo.ModeWorkerStall),
		"error_rate":    0.12,
		"latency_ms":    430,
		"queue_backlog": 128,
	})
	if err != nil {
		t.Fatalf("marshal evidence metadata: %v", err)
	}
	healthMetadata, err := json.Marshal(map[string]any{
		"mode":          string(demo.ModeWorkerStall),
		"workerHealthy": false,
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
			Snippet:      "error_rate=0.12 latency_ms=430 queue_backlog=128",
			Timestamp:    time.Now().UTC(),
			MetadataJSON: string(evidenceMetadata),
		},
		{
			ID:           uuid.NewString(),
			IncidentID:   incidentRecord.ID,
			Type:         "log",
			Source:       "demo-service",
			Snippet:      "worker stalled while queue backlog kept growing",
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

	rollbacker := execution.NewRollbackService(repository, execution.DefaultCatalog(), &fakeRollbackAdapter{})
	service := NewService(repository, stubFetcher{snapshot: demo.Snapshot{
		Mode:           demo.ModeWorkerStall,
		ErrorRate:      0.20,
		LatencyMs:      750,
		QueueBacklog:   160,
		ConsumerPaused: true,
		WorkerHealthy:  false,
		LastDeploy:     "v1.2.3",
		LastUpdatedUTC: time.Now().UTC(),
	}}, execution.DefaultCatalog(), rollbacker)

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
	if updatedIncident.State != domain.IncidentStateRolledBack {
		t.Fatalf("incident state = %q, want %q", updatedIncident.State, domain.IncidentStateRolledBack)
	}

	rollbackRecords, err := repository.ListRollbackRecordsByAction(context.Background(), action.ID)
	if err != nil {
		t.Fatalf("list rollback records: %v", err)
	}
	if len(rollbackRecords) != 1 || rollbackRecords[0].Status != execution.RollbackStatusSucceeded {
		t.Fatalf("rollback records = %#v, want one succeeded rollback", rollbackRecords)
	}
	if strings.Contains(result.EvidenceJSON, `"escalation_recommended":true`) {
		t.Fatalf("verification evidence should not keep recommending escalation after successful rollback: %s", result.EvidenceJSON)
	}
	if !strings.Contains(result.Notes, "automatic rollback succeeded") {
		t.Fatalf("verification notes should mention successful automatic rollback, got %q", result.Notes)
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

type fakeRollbackAdapter struct{}

func (f *fakeRollbackAdapter) Execute(ctx context.Context, action domain.CandidateAction, request execution.AdapterRequest) (execution.AdapterResult, error) {
	return execution.AdapterResult{
		ExecutorType: "fake-rollback-adapter",
		Payload: map[string]any{
			"action":  action.ActionType,
			"applied": true,
		},
	}, nil
}
