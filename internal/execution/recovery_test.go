package execution

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/storage"
)

type recoveryAdapter struct{ status ReconciliationStatus }

func (a recoveryAdapter) Execute(context.Context, domain.CandidateAction, AdapterRequest) (AdapterResult, error) {
	return AdapterResult{}, nil
}

func (a recoveryAdapter) Reconcile(context.Context, domain.CandidateAction, AdapterRequest) (AdapterResult, ReconciliationStatus, error) {
	return AdapterResult{ExecutorType: "recovery-test", Payload: map[string]any{"confirmed": true}}, a.status, nil
}

func TestRecoverStartedExecutionFinalizesConfirmedExternalEffect(t *testing.T) {
	repository := storage.NewMemoryStore()
	incidentRecord, action := seedApprovedExecutionFixture(t, repository)
	now := time.Now().UTC()
	record := domain.ExecutionRecord{ID: "execution-started", CandidateActionID: action.ID, IdempotencyKey: "execute:" + action.ID, InitiatedBy: "operator", ExecutorType: "workload-control-api", Status: ExecutionStatusStarted, StartedAt: now, FinishedAt: now, ResultJSON: `{}`}
	claimed, err := repository.ClaimExecution(context.Background(), incidentRecord.ID, action.ID, record)
	if err != nil || !claimed {
		t.Fatalf("claim execution: claimed=%t err=%v", claimed, err)
	}
	service := NewService(repository, DefaultCatalog(), recoveryAdapter{status: ReconciliationSucceeded}, nil, nil, time.Second, 0, time.Minute)
	recovered, err := service.RecoverStartedExecutions(context.Background())
	if err != nil {
		t.Fatalf("recover executions: %v", err)
	}
	if recovered != 1 {
		t.Fatalf("recovered = %d, want 1", recovered)
	}
	records, _ := repository.ListExecutionRecordsByAction(context.Background(), action.ID)
	if records[0].Status != ExecutionStatusSucceeded {
		t.Fatalf("status = %q, want succeeded", records[0].Status)
	}
	incidentRecord, _ = repository.GetIncident(context.Background(), incidentRecord.ID)
	if incidentRecord.State != domain.IncidentStateVerifyingAction {
		t.Fatalf("incident state = %q, want verifying_action", incidentRecord.State)
	}
}

func TestRecoverStartedExecutionEscalatesUnknownPreDispatchWindow(t *testing.T) {
	repository := storage.NewMemoryStore()
	incidentRecord, action := seedApprovedExecutionFixture(t, repository)
	now := time.Now().UTC()
	record := domain.ExecutionRecord{ID: "execution-before-dispatch", CandidateActionID: action.ID, IdempotencyKey: "execute:" + action.ID, InitiatedBy: "operator", ExecutorType: "workload-control-api", Status: ExecutionStatusStarted, StartedAt: now, FinishedAt: now, ResultJSON: `{}`}
	claimed, err := repository.ClaimExecution(context.Background(), incidentRecord.ID, action.ID, record)
	if err != nil || !claimed {
		t.Fatalf("claim execution: claimed=%t err=%v", claimed, err)
	}
	service := NewService(repository, DefaultCatalog(), recoveryAdapter{status: ReconciliationUnknown}, nil, nil, time.Second, 0, time.Minute)
	if recovered, err := service.RecoverStartedExecutions(context.Background()); err != nil || recovered != 1 {
		t.Fatalf("recover executions: recovered=%d err=%v", recovered, err)
	}
	records, _ := repository.ListExecutionRecordsByAction(context.Background(), action.ID)
	if records[0].Status != ExecutionStatusInconclusive {
		t.Fatalf("status = %q, want inconclusive", records[0].Status)
	}
	updated, _ := repository.GetIncident(context.Background(), incidentRecord.ID)
	if updated.State != domain.IncidentStateEscalated {
		t.Fatalf("incident state = %q, want escalated", updated.State)
	}
}

func TestControlAdapterReconcilesOperationStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/operations/execute:action-1" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Fatal("missing authorization")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"operation_id":"execute:action-1","status":"succeeded"}`))
	}))
	defer server.Close()
	adapter := NewControlAdapter(server.URL, "token")
	_, status, err := adapter.Reconcile(context.Background(), domain.CandidateAction{}, AdapterRequest{IdempotencyKey: "execute:action-1"})
	if err != nil {
		t.Fatal(err)
	}
	if status != ReconciliationSucceeded {
		t.Fatalf("status = %q, want succeeded", status)
	}
}
