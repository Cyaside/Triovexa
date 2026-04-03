package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/Cyaside/Triovexa/internal/domain"
)

const (
	RollbackStatusStarted   = "started"
	RollbackStatusSucceeded = "succeeded"
	RollbackStatusFailed    = "failed"
	defaultRollbackTimeout  = 5 * time.Second
)

type RollbackService struct {
	repository interface {
		SaveRollbackRecord(context.Context, domain.RollbackRecord) error
		UpdateCandidateActionStatus(context.Context, string, domain.CandidateActionStatus) error
	}
	catalog Catalog
	adapter Adapter
	timeout time.Duration
	now     func() time.Time
}

func NewRollbackService(
	repository interface {
		SaveRollbackRecord(context.Context, domain.RollbackRecord) error
		UpdateCandidateActionStatus(context.Context, string, domain.CandidateActionStatus) error
	},
	catalog Catalog,
	adapter Adapter,
	timeouts ...time.Duration,
) *RollbackService {
	timeout := defaultRollbackTimeout
	if len(timeouts) > 0 && timeouts[0] > 0 {
		timeout = timeouts[0]
	}

	return &RollbackService{
		repository: repository,
		catalog:    catalog,
		adapter:    adapter,
		timeout:    timeout,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
}

func (s *RollbackService) RollbackAction(ctx context.Context, action domain.CandidateAction, triggeredBy string, note string) (domain.RollbackRecord, error) {
	if s.adapter == nil {
		return domain.RollbackRecord{}, fmt.Errorf("rollback adapter is not configured")
	}
	if triggeredBy == "" {
		return domain.RollbackRecord{}, fmt.Errorf("triggered_by is required")
	}

	definition, ok := s.catalog.Get(action.ActionType)
	if !ok {
		return domain.RollbackRecord{}, fmt.Errorf("candidate action %q is not in catalog", action.ActionType)
	}
	if !definition.SupportsRollback || definition.RollbackActionKey == "" {
		return domain.RollbackRecord{}, fmt.Errorf("candidate action %q does not have a rollback plan", action.ActionType)
	}

	rollbackDefinition, ok := s.catalog.Get(definition.RollbackActionKey)
	if !ok {
		return domain.RollbackRecord{}, fmt.Errorf("rollback action %q is not in catalog", definition.RollbackActionKey)
	}
	if !rollbackDefinition.Executable {
		return domain.RollbackRecord{}, fmt.Errorf("rollback action %q is not executable", definition.RollbackActionKey)
	}

	record := domain.RollbackRecord{
		ID:                uuid.NewString(),
		CandidateActionID: action.ID,
		RollbackActionKey: definition.RollbackActionKey,
		TriggeredBy:       triggeredBy,
		Status:            RollbackStatusStarted,
		StartedAt:         s.now(),
		FinishedAt:        s.now(),
		ResultJSON:        `{"status":"rollback_requested"}`,
		Note:              note,
	}
	if err := s.repository.SaveRollbackRecord(ctx, record); err != nil {
		return domain.RollbackRecord{}, fmt.Errorf("save rollback record: %w", err)
	}

	rollbackAction := domain.CandidateAction{
		ID:             "rollback-" + action.ID,
		IncidentID:     action.IncidentID,
		ActionType:     definition.RollbackActionKey,
		TargetResource: action.TargetResource,
		ParametersJSON: "{}",
		RiskLevel:      rollbackDefinition.RiskLevel,
		Rationale:      fmt.Sprintf("rollback for %s", action.ActionType),
		CreatedAt:      s.now(),
	}

	result, err := s.adapter.Execute(ctx, rollbackAction, AdapterRequest{
		IdempotencyKey: "rollback:" + action.ID,
		Timeout:        s.timeout,
		InitiatedBy:    triggeredBy,
	})
	record.FinishedAt = s.now()
	if err != nil {
		record.Status = RollbackStatusFailed
		record.ResultJSON = marshalRollbackPayload(map[string]any{
			"error": err.Error(),
		})
		if saveErr := s.repository.SaveRollbackRecord(ctx, record); saveErr != nil {
			return domain.RollbackRecord{}, fmt.Errorf("update failed rollback record: %w", saveErr)
		}
		return record, err
	}

	record.Status = RollbackStatusSucceeded
	record.ResultJSON = marshalRollbackPayload(result.Payload)
	if err := s.repository.SaveRollbackRecord(ctx, record); err != nil {
		return domain.RollbackRecord{}, fmt.Errorf("update rollback record: %w", err)
	}
	if err := s.repository.UpdateCandidateActionStatus(ctx, action.ID, domain.CandidateActionStatusRolledBack); err != nil {
		return domain.RollbackRecord{}, fmt.Errorf("mark candidate action rolled back: %w", err)
	}

	return record, nil
}

func marshalRollbackPayload(payload map[string]any) string {
	if payload == nil {
		payload = map[string]any{}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "{}"
	}

	return string(body)
}
