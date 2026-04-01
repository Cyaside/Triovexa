package execution

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/storage"
)

func TestRollbackServiceRollbackActionSuccess(t *testing.T) {
	t.Parallel()

	repository := storage.NewMemoryStore()
	action := seedRollbackFixture(t, repository)

	service := NewRollbackService(repository, DefaultCatalog(), &fakeAdapter{
		exec: func(ctx context.Context, action domain.CandidateAction, request AdapterRequest) (AdapterResult, error) {
			if action.ActionType != "resume_demo_queue_consumer" {
				t.Fatalf("rollback action type = %q, want %q", action.ActionType, "resume_demo_queue_consumer")
			}
			return AdapterResult{
				ExecutorType: "fake-adapter",
				Payload: map[string]any{
					"applied": true,
				},
			}, nil
		},
	})

	record, err := service.RollbackAction(context.Background(), action, "operator-a", "consumer pause made things worse")
	if err != nil {
		t.Fatalf("rollback action: %v", err)
	}

	if record.Status != RollbackStatusSucceeded {
		t.Fatalf("rollback status = %q, want %q", record.Status, RollbackStatusSucceeded)
	}

	updatedAction, err := repository.GetCandidateAction(context.Background(), action.ID)
	if err != nil {
		t.Fatalf("get candidate action: %v", err)
	}
	if updatedAction.Status != domain.CandidateActionStatusRolledBack {
		t.Fatalf("action status = %q, want %q", updatedAction.Status, domain.CandidateActionStatusRolledBack)
	}
}

func seedRollbackFixture(t *testing.T, repository storage.Repository) domain.CandidateAction {
	t.Helper()

	incidentRecord := domain.Incident{
		ID:          "incident-rollback-1",
		Environment: "staging",
		State:       domain.IncidentStateFailedRemediation,
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
		Status:         domain.CandidateActionStatusFailed,
		CreatedAt:      time.Now().UTC(),
	}
	if err := repository.SaveCandidateActions(context.Background(), []domain.CandidateAction{action}); err != nil {
		t.Fatalf("save candidate action: %v", err)
	}

	return action
}
