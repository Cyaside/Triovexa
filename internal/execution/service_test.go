package execution

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/storage"
)

type staticKillSwitch struct {
	enabled bool
}

func (s staticKillSwitch) Enabled() bool {
	return s.enabled
}

type fakeAdapter struct {
	calls int
	exec  func(context.Context, domain.CandidateAction, AdapterRequest) (AdapterResult, error)
}

func (f *fakeAdapter) Execute(ctx context.Context, action domain.CandidateAction, request AdapterRequest) (AdapterResult, error) {
	f.calls++
	return f.exec(ctx, action, request)
}

func TestServiceExecuteActionSuccess(t *testing.T) {
	t.Parallel()

	repository := storage.NewMemoryStore()
	incidentRecord, action := seedApprovedExecutionFixture(t, repository)

	adapter := &fakeAdapter{
		exec: func(ctx context.Context, action domain.CandidateAction, request AdapterRequest) (AdapterResult, error) {
			return AdapterResult{
				ExecutorType: "fake-adapter",
				Payload: map[string]any{
					"applied": true,
				},
			}, nil
		},
	}

	service := NewService(repository, DefaultCatalog(), adapter, staticKillSwitch{}, nil, 2*time.Second, 1, time.Minute)
	record, err := service.ExecuteAction(context.Background(), action.ID, "operator-a")
	if err != nil {
		t.Fatalf("execute action: %v", err)
	}

	if record.Status != ExecutionStatusSucceeded {
		t.Fatalf("execution status = %q, want %q", record.Status, ExecutionStatusSucceeded)
	}

	storedAction, err := repository.GetCandidateAction(context.Background(), action.ID)
	if err != nil {
		t.Fatalf("get candidate action: %v", err)
	}
	if storedAction.Status != domain.CandidateActionStatusSucceeded {
		t.Fatalf("action status = %q, want %q", storedAction.Status, domain.CandidateActionStatusSucceeded)
	}

	updatedIncident, err := repository.GetIncident(context.Background(), incidentRecord.ID)
	if err != nil {
		t.Fatalf("get incident: %v", err)
	}
	if updatedIncident.State != domain.IncidentStateVerifyingAction {
		t.Fatalf("incident state = %q, want %q", updatedIncident.State, domain.IncidentStateVerifyingAction)
	}
}

func TestServiceExecuteActionRetriesRetryableError(t *testing.T) {
	t.Parallel()

	repository := storage.NewMemoryStore()
	_, action := seedApprovedExecutionFixture(t, repository)

	adapter := &fakeAdapter{
		exec: func(ctx context.Context, action domain.CandidateAction, request AdapterRequest) (AdapterResult, error) {
			if request.IdempotencyKey == "" || request.InitiatedBy == "" {
				t.Fatalf("expected execution request metadata to be forwarded")
			}
			if request.Timeout != 2*time.Second {
				t.Fatalf("timeout = %s, want %s", request.Timeout, 2*time.Second)
			}
			if action.Status != domain.CandidateActionStatusApproved {
				t.Fatalf("expected approved action")
			}
			return AdapterResult{}, RetryableError{Err: errors.New("temporary network issue")}
		},
	}

	service := NewService(repository, DefaultCatalog(), adapter, staticKillSwitch{}, nil, 2*time.Second, 1, time.Minute)
	_, err := service.ExecuteAction(context.Background(), action.ID, "operator-a")
	if err == nil {
		t.Fatalf("expected execution error")
	}
	if adapter.calls != 2 {
		t.Fatalf("adapter calls = %d, want %d", adapter.calls, 2)
	}
}

func TestServiceExecuteActionBlocksKillSwitchAndDuplicates(t *testing.T) {
	t.Parallel()

	repository := storage.NewMemoryStore()
	_, action := seedApprovedExecutionFixture(t, repository)

	service := NewService(repository, DefaultCatalog(), &fakeAdapter{
		exec: func(ctx context.Context, action domain.CandidateAction, request AdapterRequest) (AdapterResult, error) {
			return AdapterResult{ExecutorType: "fake-adapter"}, nil
		},
	}, staticKillSwitch{enabled: true}, nil, 2*time.Second, 0, time.Minute)

	if _, err := service.ExecuteAction(context.Background(), action.ID, "operator-a"); err == nil {
		t.Fatalf("expected kill switch error")
	}

	service = NewService(repository, DefaultCatalog(), &fakeAdapter{
		exec: func(ctx context.Context, action domain.CandidateAction, request AdapterRequest) (AdapterResult, error) {
			return AdapterResult{ExecutorType: "fake-adapter"}, nil
		},
	}, staticKillSwitch{}, nil, 2*time.Second, 0, time.Minute)

	first, err := service.ExecuteAction(context.Background(), action.ID, "operator-a")
	if err != nil {
		t.Fatalf("first execute action: %v", err)
	}

	if _, err := service.ExecuteAction(context.Background(), action.ID, "operator-a"); err == nil {
		t.Fatalf("expected duplicate execution prevention error")
	}

	records, err := repository.ListExecutionRecordsByAction(context.Background(), action.ID)
	if err != nil {
		t.Fatalf("list execution records by action: %v", err)
	}
	if len(records) != 1 || records[0].ID != first.ID {
		t.Fatalf("execution records = %#v, want the original execution record", records)
	}
}

func TestServiceExecuteActionAllowsApprovedMediumRisk(t *testing.T) {
	t.Parallel()

	repository := storage.NewMemoryStore()
	incidentRecord := domain.Incident{
		ID:          "incident-medium-1",
		Environment: "staging",
		State:       domain.IncidentStateApproved,
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
		EvidenceRefs:   []string{},
		Status:         domain.CandidateActionStatusApproved,
		CreatedAt:      time.Now().UTC(),
	}
	if err := repository.SaveCandidateActions(context.Background(), []domain.CandidateAction{action}); err != nil {
		t.Fatalf("save candidate action: %v", err)
	}

	service := NewService(repository, DefaultCatalog(), &fakeAdapter{
		exec: func(ctx context.Context, action domain.CandidateAction, request AdapterRequest) (AdapterResult, error) {
			return AdapterResult{ExecutorType: "fake-adapter", Payload: map[string]any{"applied": true}}, nil
		},
	}, staticKillSwitch{}, nil, 2*time.Second, 0, time.Minute)

	record, err := service.ExecuteAction(context.Background(), action.ID, "operator-a")
	if err != nil {
		t.Fatalf("execute medium-risk action: %v", err)
	}

	if record.Status != ExecutionStatusSucceeded {
		t.Fatalf("execution status = %q, want %q", record.Status, ExecutionStatusSucceeded)
	}
}

func seedApprovedExecutionFixture(t *testing.T, repository storage.Repository) (domain.Incident, domain.CandidateAction) {
	t.Helper()

	incidentRecord := domain.Incident{
		ID:          "incident-1",
		Environment: "staging",
		State:       domain.IncidentStateApproved,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	if err := repository.CreateIncident(context.Background(), incidentRecord); err != nil {
		t.Fatalf("create incident: %v", err)
	}

	action := domain.CandidateAction{
		ID:             uuid.NewString(),
		IncidentID:     incidentRecord.ID,
		ActionType:     "restart_demo_worker",
		TargetResource: "demo-worker",
		ParametersJSON: `{"worker_id":"worker-primary"}`,
		RiskLevel:      domain.RiskLevelLow,
		Rationale:      "worker restart is safe",
		EvidenceRefs:   []string{},
		Status:         domain.CandidateActionStatusApproved,
		CreatedAt:      time.Now().UTC(),
	}
	if err := repository.SaveCandidateActions(context.Background(), []domain.CandidateAction{action}); err != nil {
		t.Fatalf("save candidate action: %v", err)
	}

	return incidentRecord, action
}
