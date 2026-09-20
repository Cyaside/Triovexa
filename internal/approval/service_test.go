package approval

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/execution"
	"github.com/Cyaside/Triovexa/internal/policy"
	"github.com/Cyaside/Triovexa/internal/storage"
)

func TestServiceEvaluateActionsMovesIncidentToAwaitingApproval(t *testing.T) {
	t.Parallel()

	repository := storage.NewMemoryStore()
	killSwitch := NewKillSwitch(false)
	service := NewService(repository, policy.NewEvaluator(execution.DefaultCatalog()), killSwitch)

	incidentRecord := domain.Incident{
		ID:          "inc-1",
		Environment: "staging",
		State:       domain.IncidentStateActionProposed,
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
		TargetResource: "demo-cache",
		ParametersJSON: `{"cache_key":"checkout-session"}`,
		RiskLevel:      domain.RiskLevelLow,
		Rationale:      "cache refresh is safe",
		Status:         domain.CandidateActionStatusProposed,
		CreatedAt:      time.Now().UTC(),
	}
	if err := repository.SaveCandidateActions(context.Background(), []domain.CandidateAction{action}); err != nil {
		t.Fatalf("save candidate action: %v", err)
	}

	updatedIncident, err := service.EvaluateActions(context.Background(), incidentRecord, []domain.CandidateAction{action})
	if err != nil {
		t.Fatalf("evaluate actions: %v", err)
	}

	if updatedIncident.State != domain.IncidentStateAwaitingApproval {
		t.Fatalf("incident state = %q, want %q", updatedIncident.State, domain.IncidentStateAwaitingApproval)
	}

	storedAction, err := repository.GetCandidateAction(context.Background(), action.ID)
	if err != nil {
		t.Fatalf("get candidate action: %v", err)
	}

	if storedAction.Status != domain.CandidateActionStatusAwaitingApproval {
		t.Fatalf("action status = %q, want %q", storedAction.Status, domain.CandidateActionStatusAwaitingApproval)
	}

	decision, err := repository.GetPolicyDecision(context.Background(), action.ID)
	if err != nil {
		t.Fatalf("get policy decision: %v", err)
	}

	if decision.Decision != domain.PolicyDecisionApprovalRequired {
		t.Fatalf("policy decision = %q, want %q", decision.Decision, domain.PolicyDecisionApprovalRequired)
	}
}

func TestConcurrentApproversProduceOneAtomicDecision(t *testing.T) {
	repository := storage.NewMemoryStore()
	service := NewService(repository, policy.NewEvaluator(execution.DefaultCatalog()), NewKillSwitch(false))
	incidentRecord := domain.Incident{ID: "inc-concurrent", Environment: "staging", State: domain.IncidentStateAwaitingApproval, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := repository.CreateIncident(context.Background(), incidentRecord); err != nil {
		t.Fatal(err)
	}
	action := domain.CandidateAction{ID: uuid.NewString(), IncidentID: incidentRecord.ID, ActionType: "restart_demo_worker", TargetResource: "demo-worker", ParametersJSON: `{}`, RiskLevel: domain.RiskLevelLow, Status: domain.CandidateActionStatusAwaitingApproval, CreatedAt: time.Now().UTC()}
	if err := repository.SaveCandidateActions(context.Background(), []domain.CandidateAction{action}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SavePolicyDecisions(context.Background(), []domain.PolicyDecision{{ID: uuid.NewString(), CandidateActionID: action.ID, Decision: domain.PolicyDecisionApprovalRequired, ApprovalRequired: true, PolicyRuleRef: "policy-v1", DecidedAt: time.Now().UTC()}}); err != nil {
		t.Fatal(err)
	}
	var successes atomic.Int32
	var wait sync.WaitGroup
	for _, actor := range []string{"operator-a", "operator-b"} {
		wait.Add(1)
		go func(actor string) {
			defer wait.Done()
			if _, err := service.ApproveAction(context.Background(), action.ID, actor, "approved"); err == nil {
				successes.Add(1)
			}
		}(actor)
	}
	wait.Wait()
	if successes.Load() != 1 {
		t.Fatalf("successful approvals = %d, want 1", successes.Load())
	}
	records, _ := repository.ListApprovalRecords(context.Background(), incidentRecord.ID)
	if len(records) != 1 {
		t.Fatalf("approval records = %d, want 1", len(records))
	}
}

func TestKillSwitchStatePersistsAcrossServiceRestart(t *testing.T) {
	repository := storage.NewMemoryStore()
	first := NewService(repository, policy.NewEvaluator(execution.DefaultCatalog()), NewKillSwitch(false))
	if state := first.SetKillSwitch(true); !state.Enabled {
		t.Fatal("kill switch was not enabled")
	}
	second := NewService(repository, policy.NewEvaluator(execution.DefaultCatalog()), NewKillSwitch(false))
	if !second.KillSwitchState().Enabled {
		t.Fatal("persisted kill switch state was not restored")
	}
}

func TestServiceApproveActionPersistsApprovalRecord(t *testing.T) {
	t.Parallel()

	repository := storage.NewMemoryStore()
	service := NewService(repository, policy.NewEvaluator(execution.DefaultCatalog()), NewKillSwitch(false))

	incidentRecord := domain.Incident{
		ID:          "inc-2",
		Environment: "staging",
		State:       domain.IncidentStateAwaitingApproval,
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
		Rationale:      "restart the worker",
		Status:         domain.CandidateActionStatusAwaitingApproval,
		CreatedAt:      time.Now().UTC(),
	}
	if err := repository.SaveCandidateActions(context.Background(), []domain.CandidateAction{action}); err != nil {
		t.Fatalf("save candidate action: %v", err)
	}
	if err := repository.SavePolicyDecisions(context.Background(), []domain.PolicyDecision{{
		ID:                uuid.NewString(),
		CandidateActionID: action.ID,
		Decision:          domain.PolicyDecisionApprovalRequired,
		Reason:            "approval required",
		ApprovalRequired:  true,
		PolicyRuleRef:     "risk/low-requires-approval",
		DecidedAt:         time.Now().UTC(),
	}}); err != nil {
		t.Fatalf("save policy decision: %v", err)
	}

	approvedAction, err := service.ApproveAction(context.Background(), action.ID, "operator-a", "looks safe")
	if err != nil {
		t.Fatalf("approve action: %v", err)
	}

	if approvedAction.Status != domain.CandidateActionStatusApproved {
		t.Fatalf("action status = %q, want %q", approvedAction.Status, domain.CandidateActionStatusApproved)
	}

	records, err := repository.ListApprovalRecords(context.Background(), incidentRecord.ID)
	if err != nil {
		t.Fatalf("list approval records: %v", err)
	}

	if len(records) != 1 || records[0].Decision != "approved" {
		t.Fatalf("approval records = %#v, want one approved record", records)
	}
}

func TestServiceRejectsApprovalAfterIncidentBecomesTerminal(t *testing.T) {
	t.Parallel()

	repository := storage.NewMemoryStore()
	service := NewService(repository, policy.NewEvaluator(execution.DefaultCatalog()), NewKillSwitch(false))
	incidentRecord := domain.Incident{ID: "inc-terminal", Environment: "staging", State: domain.IncidentStateClosed, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := repository.CreateIncident(context.Background(), incidentRecord); err != nil {
		t.Fatal(err)
	}
	action := domain.CandidateAction{ID: uuid.NewString(), IncidentID: incidentRecord.ID, ActionType: "restart_worker", TargetResource: "queue-worker", ParametersJSON: `{"worker_id":"queue-worker"}`, RiskLevel: domain.RiskLevelLow, Status: domain.CandidateActionStatusAwaitingApproval, CreatedAt: time.Now().UTC()}
	if err := repository.SaveCandidateActions(context.Background(), []domain.CandidateAction{action}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SavePolicyDecisions(context.Background(), []domain.PolicyDecision{{ID: uuid.NewString(), CandidateActionID: action.ID, Decision: domain.PolicyDecisionApprovalRequired, ApprovalRequired: true, PolicyRuleRef: "risk/low-requires-approval", DecidedAt: time.Now().UTC()}}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApproveAction(context.Background(), action.ID, "operator-a", "stale decision"); err == nil || !strings.Contains(err.Error(), "terminal state") {
		t.Fatalf("approve terminal incident error = %v, want terminal-state rejection", err)
	}
	records, _ := repository.ListApprovalRecords(context.Background(), incidentRecord.ID)
	if len(records) != 0 {
		t.Fatalf("approval records = %#v, want none", records)
	}
}
