package verification

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Cyaside/Triovexa/internal/demo"
	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/execution"
	"github.com/Cyaside/Triovexa/internal/storage"
	"github.com/Cyaside/Triovexa/internal/telemetry"
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

func TestServiceVerifyExecutionAcceptsStableWorkloadRecovery(t *testing.T) {
	t.Parallel()

	repository := storage.NewMemoryStore()
	incidentRecord, action, executionRecord := seedVerificationFixture(t, repository)
	action.ActionType = "restart_worker"
	action.TargetResource = "queue-worker"
	if err := repository.SaveCandidateActions(context.Background(), []domain.CandidateAction{action}); err != nil {
		t.Fatal(err)
	}
	workloadBaseline, _ := json.Marshal(map[string]any{
		"mode": string(demo.ModeWorkerStall), "error_rate": 0.12, "latency_ms": 430,
		"queue_backlog": 42, "workerHealthy": false,
	})
	if err := repository.SaveEvidenceItems(context.Background(), []domain.EvidenceItem{{
		ID: uuid.NewString(), IncidentID: incidentRecord.ID, Type: "metric", Source: "workload-control",
		Timestamp: time.Now().UTC(), MetadataJSON: string(workloadBaseline),
	}}); err != nil {
		t.Fatal(err)
	}

	service := NewService(repository, stubFetcher{snapshot: demo.Snapshot{
		Mode: demo.ModeHealthy, ErrorRate: 0.12, LatencyMs: 430, QueueBacklog: 4,
		WorkerHealthy: true, LastUpdatedUTC: time.Now().UTC(),
	}}, execution.DefaultCatalog(), nil)

	result, err := service.VerifyExecution(context.Background(), action, executionRecord)
	if err != nil {
		t.Fatalf("verify execution: %v", err)
	}
	if result.Status != StatusSuccess {
		t.Fatalf("verification status = %q, want %q", result.Status, StatusSuccess)
	}
	updated, err := repository.GetIncident(context.Background(), incidentRecord.ID)
	if err != nil || updated.State != domain.IncidentStateResolved {
		t.Fatalf("incident = %#v err=%v, want resolved", updated, err)
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

func TestServiceVerifyExecutionEscalatesWhenCompensationFails(t *testing.T) {
	repository := storage.NewMemoryStore()
	now := time.Now().UTC()
	incidentRecord := domain.Incident{ID: "incident-compensation-failure", Title: "queue consumer backlog", ServiceName: "queue-worker", Environment: "staging", State: domain.IncidentStateVerifyingAction, CreatedAt: now, UpdatedAt: now}
	if err := repository.CreateIncident(context.Background(), incidentRecord); err != nil {
		t.Fatal(err)
	}
	action := domain.CandidateAction{ID: uuid.NewString(), IncidentID: incidentRecord.ID, ActionType: "pause_demo_queue_consumer", TargetResource: "demo-queue-consumer", ParametersJSON: `{}`, RiskLevel: domain.RiskLevelMedium, Status: domain.CandidateActionStatusSucceeded, CreatedAt: now}
	if err := repository.SaveCandidateActions(context.Background(), []domain.CandidateAction{action}); err != nil {
		t.Fatal(err)
	}
	metadata, _ := json.Marshal(map[string]any{"mode": string(demo.ModeWorkerStall), "error_rate": 0.12, "latency_ms": 430, "queue_backlog": 128, "workerHealthy": false, "complete": true})
	if err := repository.SaveEvidenceItems(context.Background(), []domain.EvidenceItem{{ID: uuid.NewString(), IncidentID: incidentRecord.ID, Type: "metric", Source: "workload-control", Timestamp: now, MetadataJSON: string(metadata)}}); err != nil {
		t.Fatal(err)
	}
	record := domain.ExecutionRecord{ID: uuid.NewString(), CandidateActionID: action.ID, IdempotencyKey: "execute:" + action.ID, InitiatedBy: "operator", ExecutorType: "fake", Status: "succeeded", StartedAt: now, FinishedAt: now, ResultJSON: `{}`}
	if err := repository.SaveExecutionRecord(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	rollbacker := execution.NewRollbackService(repository, execution.DefaultCatalog(), &fakeRollbackAdapter{err: errors.New("resume operation rejected")})
	service := NewService(repository, stubFetcher{snapshot: demo.Snapshot{Mode: demo.ModeWorkerStall, ErrorRate: 0.20, LatencyMs: 750, QueueBacklog: 160, ConsumerPaused: true, WorkerHealthy: false, LastUpdatedUTC: now}}, execution.DefaultCatalog(), rollbacker)
	result, err := service.VerifyExecution(context.Background(), action, record)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusFailed || !strings.Contains(result.EvidenceJSON, `"rollback_succeeded":false`) {
		t.Fatalf("unexpected verification result: %#v", result)
	}
	updated, _ := repository.GetIncident(context.Background(), incidentRecord.ID)
	if updated.State != domain.IncidentStateEscalated {
		t.Fatalf("incident state = %q, want escalated", updated.State)
	}
	rollbacks, _ := repository.ListRollbackRecordsByAction(context.Background(), action.ID)
	if len(rollbacks) != 1 || rollbacks[0].Status != execution.RollbackStatusFailed {
		t.Fatalf("rollback records = %#v", rollbacks)
	}
}

func TestServiceVerifyExecutionTelemetryMarksErrorsExplicitly(t *testing.T) {
	t.Parallel()

	baseRepository := storage.NewMemoryStore()
	_, action, executionRecord := seedVerificationFixture(t, baseRepository)
	recorder := telemetry.NewRecorder()

	service := NewService(auditFailingVerificationRepository{Repository: baseRepository}, stubFetcher{
		snapshot: demo.Snapshot{
			Mode:           demo.ModeHealthy,
			ErrorRate:      0.02,
			LatencyMs:      180,
			QueueBacklog:   5,
			WorkerHealthy:  true,
			LastDeploy:     "v1.2.3",
			LastUpdatedUTC: time.Now().UTC(),
		},
	}, execution.DefaultCatalog(), nil).WithTelemetry(recorder)

	if _, err := service.VerifyExecution(context.Background(), action, executionRecord); err == nil {
		t.Fatalf("expected verification error")
	}

	response := httptest.NewRecorder()
	recorder.ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	body := response.Body.String()

	if !strings.Contains(body, `triovexa_verification_outcomes_total{status="error"} 1`) {
		t.Fatalf("metrics should count verification errors explicitly, got:\n%s", body)
	}
	if strings.Contains(body, `triovexa_verification_outcomes_total{status="inconclusive"} 1`) {
		t.Fatalf("metrics should not misclassify verification errors as inconclusive, got:\n%s", body)
	}
}

func TestServiceVerifyExecutionPreservesBaselineSnapshotFields(t *testing.T) {
	t.Parallel()

	repository := storage.NewMemoryStore()
	_, action, executionRecord := seedVerificationFixture(t, repository)

	service := NewService(repository, stubFetcher{snapshot: demo.Snapshot{
		Mode:           demo.ModeHealthy,
		ErrorRate:      0.02,
		LatencyMs:      180,
		QueueBacklog:   5,
		ReplicaCount:   2,
		ConsumerPaused: false,
		WorkerHealthy:  true,
		LastDeploy:     "v1.2.3",
		LastUpdatedUTC: time.Now().UTC(),
	}}, execution.DefaultCatalog(), nil)

	result, err := service.VerifyExecution(context.Background(), action, executionRecord)
	if err != nil {
		t.Fatalf("verify execution: %v", err)
	}

	var evidence map[string]any
	if err := json.Unmarshal([]byte(result.EvidenceJSON), &evidence); err != nil {
		t.Fatalf("unmarshal verification evidence: %v", err)
	}

	before, ok := evidence["before"].(map[string]any)
	if !ok {
		t.Fatalf("verification evidence before snapshot missing or invalid: %#v", evidence["before"])
	}

	if got, want := int(before["replica_count"].(float64)), 2; got != want {
		t.Fatalf("before replica_count = %d, want %d", got, want)
	}
	if got, want := before["consumer_paused"].(bool), false; got != want {
		t.Fatalf("before consumer_paused = %t, want %t", got, want)
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
		"mode":            string(demo.ModeTimeoutAfterDeploy),
		"error_rate":      0.27,
		"latency_ms":      1200,
		"queue_backlog":   46,
		"replica_count":   2,
		"consumer_paused": false,
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

type fakeRollbackAdapter struct{ err error }

func (f *fakeRollbackAdapter) Execute(ctx context.Context, action domain.CandidateAction, request execution.AdapterRequest) (execution.AdapterResult, error) {
	if f.err != nil {
		return execution.AdapterResult{ExecutorType: "fake-rollback-adapter"}, f.err
	}
	return execution.AdapterResult{
		ExecutorType: "fake-rollback-adapter",
		Payload: map[string]any{
			"action":  action.ActionType,
			"applied": true,
		},
	}, nil
}

type auditFailingVerificationRepository struct {
	storage.Repository
}

func (r auditFailingVerificationRepository) AddAuditEvent(context.Context, domain.AuditEvent) error {
	return errors.New("audit store unavailable")
}
