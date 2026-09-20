package execution

import (
	"context"
	"errors"
	"strings"
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

func TestServiceRejectsStaleEvidenceBeforeDispatch(t *testing.T) {
	repository := storage.NewMemoryStore()
	_, action := seedApprovedExecutionFixture(t, repository)
	adapter := &fakeAdapter{exec: func(context.Context, domain.CandidateAction, AdapterRequest) (AdapterResult, error) {
		return AdapterResult{ExecutorType: "fake"}, nil
	}}
	service := NewService(repository, DefaultCatalog(), adapter, staticKillSwitch{}, nil, time.Second, 0, time.Minute)
	service.now = func() time.Time { return time.Now().UTC().Add(2 * time.Minute) }
	_, err := service.ExecuteAction(context.Background(), action.ID, "operator-a")
	if err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("error = %v, want stale evidence rejection", err)
	}
	if adapter.calls != 0 {
		t.Fatalf("adapter calls = %d, want 0", adapter.calls)
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
		EvidenceRefs:   []string{"evidence-fresh"},
		Status:         domain.CandidateActionStatusApproved,
		CreatedAt:      time.Now().UTC(),
	}
	if err := repository.SaveEvidenceItems(context.Background(), []domain.EvidenceItem{{
		ID: "evidence-fresh", IncidentID: incidentRecord.ID, Type: "metric", Source: "test",
		Snippet: "worker unhealthy", Timestamp: time.Now().UTC(), MetadataJSON: `{"complete":true}`,
	}}); err != nil {
		t.Fatalf("save evidence: %v", err)
	}
	if err := repository.SaveCandidateActions(context.Background(), []domain.CandidateAction{action}); err != nil {
		t.Fatalf("save candidate action: %v", err)
	}
	seedExecutionApproval(t, repository, action)

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

func TestServiceExecuteActionRevalidatesScopeBeforeExecution(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name            string
		environment     string
		targetResource  string
		wantErrorSubstr string
	}{
		{
			name:            "environment no longer allowed",
			environment:     "production",
			targetResource:  "demo-worker",
			wantErrorSubstr: `environment "production"`,
		},
		{
			name:            "target no longer allowed",
			environment:     "staging",
			targetResource:  "unknown-target",
			wantErrorSubstr: `target "unknown-target"`,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			repository := storage.NewMemoryStore()
			incidentRecord := domain.Incident{
				ID:          uuid.NewString(),
				Environment: tc.environment,
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
				TargetResource: tc.targetResource,
				ParametersJSON: `{"worker_id":"worker-primary"}`,
				RiskLevel:      domain.RiskLevelLow,
				Rationale:      "revalidate execution scope",
				Status:         domain.CandidateActionStatusApproved,
				CreatedAt:      time.Now().UTC(),
			}
			if err := repository.SaveCandidateActions(context.Background(), []domain.CandidateAction{action}); err != nil {
				t.Fatalf("save candidate action: %v", err)
			}
			seedExecutionApproval(t, repository, action)

			adapter := &fakeAdapter{
				exec: func(ctx context.Context, action domain.CandidateAction, request AdapterRequest) (AdapterResult, error) {
					t.Fatalf("adapter should not execute when scope validation fails")
					return AdapterResult{}, nil
				},
			}

			service := NewService(repository, DefaultCatalog(), adapter, staticKillSwitch{}, nil, 2*time.Second, 0, time.Minute)
			_, err := service.ExecuteAction(context.Background(), action.ID, "operator-a")
			if err == nil {
				t.Fatalf("expected scope validation error")
			}
			if !strings.Contains(err.Error(), tc.wantErrorSubstr) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tc.wantErrorSubstr)
			}
			if adapter.calls != 0 {
				t.Fatalf("adapter calls = %d, want 0", adapter.calls)
			}

			storedAction, err := repository.GetCandidateAction(context.Background(), action.ID)
			if err != nil {
				t.Fatalf("get candidate action: %v", err)
			}
			if storedAction.Status != domain.CandidateActionStatusApproved {
				t.Fatalf("action status = %q, want %q", storedAction.Status, domain.CandidateActionStatusApproved)
			}
		})
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
		EvidenceRefs:   []string{"evidence-fresh"},
		Status:         domain.CandidateActionStatusApproved,
		CreatedAt:      time.Now().UTC(),
	}
	if err := repository.SaveEvidenceItems(context.Background(), []domain.EvidenceItem{{
		ID: "evidence-fresh", IncidentID: incidentRecord.ID, Type: "metric", Source: "test",
		Snippet: "worker unhealthy", Timestamp: time.Now().UTC(), MetadataJSON: `{"complete":true}`,
	}}); err != nil {
		t.Fatalf("save evidence: %v", err)
	}
	if err := repository.SaveCandidateActions(context.Background(), []domain.CandidateAction{action}); err != nil {
		t.Fatalf("save candidate action: %v", err)
	}
	seedExecutionApproval(t, repository, action)

	return incidentRecord, action
}

func seedExecutionApproval(t *testing.T, repository storage.Repository, action domain.CandidateAction) {
	t.Helper()
	now := time.Now().UTC()
	policyVersion := "test-policy-v1"
	if err := repository.SavePolicyDecisions(context.Background(), []domain.PolicyDecision{{
		ID:                uuid.NewString(),
		CandidateActionID: action.ID,
		Decision:          domain.PolicyDecisionApprovalRequired,
		ApprovalRequired:  true,
		PolicyRuleRef:     policyVersion,
		DecidedAt:         now,
	}}); err != nil {
		t.Fatalf("save policy decision: %v", err)
	}
	if err := repository.CreateApprovalRecord(context.Background(), domain.ApprovalRecord{
		ID:                uuid.NewString(),
		CandidateActionID: action.ID,
		ApprovedBy:        "operator-a",
		Decision:          "approved",
		ActionDigest:      domain.ActionApprovalDigest(action, policyVersion),
		PolicyVersion:     policyVersion,
		ExpiresAt:         now.Add(15 * time.Minute),
		CreatedAt:         now,
	}); err != nil {
		t.Fatalf("create approval record: %v", err)
	}
}
