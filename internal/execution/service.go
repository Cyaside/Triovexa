package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/incident"
	"github.com/Cyaside/Triovexa/internal/storage"
	"github.com/Cyaside/Triovexa/internal/telemetry"
)

const (
	ExecutionStatusStarted   = "started"
	ExecutionStatusSucceeded = "succeeded"
	ExecutionStatusFailed    = "failed"
	ExecutionStatusTimedOut  = "timed_out"
)

type KillSwitchReader interface {
	Enabled() bool
}

type VerificationWorkflow interface {
	VerifyExecution(context.Context, domain.CandidateAction, domain.ExecutionRecord) (domain.VerificationResult, error)
}

type AdapterRequest struct {
	IdempotencyKey string
	Timeout        time.Duration
	InitiatedBy    string
}

type AdapterResult struct {
	ExecutorType string
	Payload      map[string]any
}

type Adapter interface {
	Execute(context.Context, domain.CandidateAction, AdapterRequest) (AdapterResult, error)
}

type RetryableError struct {
	Err error
}

func (e RetryableError) Error() string {
	return e.Err.Error()
}

func (e RetryableError) Unwrap() error {
	return e.Err
}

type Service struct {
	repository storage.Repository
	catalog    Catalog
	adapter    Adapter
	killSwitch KillSwitchReader
	verifier   VerificationWorkflow
	metrics    *telemetry.Recorder
	timeout    time.Duration
	retries    int
	cooldown   time.Duration
	now        func() time.Time
}

func NewService(
	repository storage.Repository,
	catalog Catalog,
	adapter Adapter,
	killSwitch KillSwitchReader,
	verifier VerificationWorkflow,
	timeout time.Duration,
	retries int,
	cooldown time.Duration,
) *Service {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	if cooldown <= 0 {
		cooldown = time.Minute
	}
	if retries < 0 {
		retries = 0
	}

	return &Service{
		repository: repository,
		catalog:    catalog,
		adapter:    adapter,
		killSwitch: killSwitch,
		verifier:   verifier,
		timeout:    timeout,
		retries:    retries,
		cooldown:   cooldown,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
}

func (s *Service) WithTelemetry(recorder *telemetry.Recorder) *Service {
	s.metrics = recorder
	return s
}

func (s *Service) ExecuteAction(ctx context.Context, actionID string, initiatedBy string) (domain.ExecutionRecord, error) {
	if s.adapter == nil {
		return domain.ExecutionRecord{}, errors.New("execution adapter is not configured")
	}
	if initiatedBy == "" {
		return domain.ExecutionRecord{}, errors.New("initiated_by is required")
	}

	action, err := s.repository.GetCandidateAction(ctx, actionID)
	if err != nil {
		return domain.ExecutionRecord{}, fmt.Errorf("get candidate action: %w", err)
	}

	definition, ok := s.catalog.Get(action.ActionType)
	if !ok {
		return domain.ExecutionRecord{}, errors.New("candidate action is not part of execution catalog")
	}
	if !definition.Executable {
		return domain.ExecutionRecord{}, errors.New("candidate action is not executable in the current phase")
	}
	if definition.RiskLevel == domain.RiskLevelHigh {
		return domain.ExecutionRecord{}, errors.New("high-risk actions cannot be executed in the current phase")
	}

	idempotencyKey := "execute:" + action.ID
	existing, err := s.repository.ListExecutionRecordsByAction(ctx, action.ID)
	if err != nil {
		return domain.ExecutionRecord{}, fmt.Errorf("list execution records: %w", err)
	}
	executionCooldown := s.cooldown
	if definition.ExecutionCooldown > 0 {
		executionCooldown = definition.ExecutionCooldown
	}
	if duplicate := findDuplicateExecution(existing, idempotencyKey, executionCooldown, s.now()); duplicate != nil {
		if auditErr := s.audit(ctx, action.IncidentID, "duplicate_execution_prevented", "completed", map[string]any{
			"candidate_action_id": action.ID,
			"execution_record_id": duplicate.ID,
		}); auditErr != nil {
			return domain.ExecutionRecord{}, fmt.Errorf("audit duplicate execution prevention: %w", auditErr)
		}
		return *duplicate, errors.New("duplicate execution prevented by idempotency guard")
	}
	if definition.MaxExecutionAttempts > 0 && countExecutionAttempts(existing) >= definition.MaxExecutionAttempts {
		if auditErr := s.audit(ctx, action.IncidentID, "execution_attempt_limit_reached", "completed", map[string]any{
			"candidate_action_id": action.ID,
			"max_attempts":        definition.MaxExecutionAttempts,
		}); auditErr != nil {
			return domain.ExecutionRecord{}, fmt.Errorf("audit execution attempt limit: %w", auditErr)
		}
		return domain.ExecutionRecord{}, errors.New("execution attempt limit reached for candidate action")
	}

	if action.Status != domain.CandidateActionStatusApproved && action.Status != domain.CandidateActionStatusAllowed {
		return domain.ExecutionRecord{}, errors.New("candidate action must be approved or allowed before execution")
	}
	if definition.RiskLevel == domain.RiskLevelMedium && action.Status != domain.CandidateActionStatusApproved {
		return domain.ExecutionRecord{}, errors.New("medium-risk action must be explicitly approved before execution")
	}
	if s.killSwitch != nil && s.killSwitch.Enabled() {
		if auditErr := s.audit(ctx, action.IncidentID, "execution_blocked_by_kill_switch", "completed", map[string]any{
			"candidate_action_id": action.ID,
		}); auditErr != nil {
			return domain.ExecutionRecord{}, fmt.Errorf("audit kill switch block: %w", auditErr)
		}
		return domain.ExecutionRecord{}, errors.New("kill switch is enabled")
	}

	incidentRecord, err := s.repository.GetIncident(ctx, action.IncidentID)
	if err != nil {
		return domain.ExecutionRecord{}, fmt.Errorf("get incident for execution: %w", err)
	}
	if err := validateExecutionScope(action, incidentRecord, definition); err != nil {
		if auditErr := s.audit(ctx, action.IncidentID, "execution_scope_validation_failed", "completed", map[string]any{
			"candidate_action_id": action.ID,
			"error":               err.Error(),
		}); auditErr != nil {
			return domain.ExecutionRecord{}, fmt.Errorf("audit execution scope validation failure: %w", auditErr)
		}
		return domain.ExecutionRecord{}, err
	}
	incidentRecord, err = s.transitionIncidentState(ctx, incidentRecord, domain.IncidentStateExecutingAction)
	if err != nil {
		return domain.ExecutionRecord{}, fmt.Errorf("move incident to executing_action: %w", err)
	}

	if err := s.repository.UpdateCandidateActionStatus(ctx, action.ID, domain.CandidateActionStatusExecuting); err != nil {
		return domain.ExecutionRecord{}, fmt.Errorf("update action status to executing: %w", err)
	}

	record := domain.ExecutionRecord{
		ID:                uuid.NewString(),
		CandidateActionID: action.ID,
		IdempotencyKey:    idempotencyKey,
		InitiatedBy:       initiatedBy,
		ExecutorType:      "demo-http-adapter",
		Status:            ExecutionStatusStarted,
		StartedAt:         s.now(),
		FinishedAt:        s.now(),
		ResultJSON:        `{"status":"execution_requested"}`,
	}
	if err := s.repository.SaveExecutionRecord(ctx, record); err != nil {
		return domain.ExecutionRecord{}, fmt.Errorf("create execution record: %w", err)
	}

	if err := s.audit(ctx, action.IncidentID, "execution_requested", "completed", map[string]any{
		"candidate_action_id": action.ID,
		"execution_record_id": record.ID,
		"initiated_by":        initiatedBy,
	}); err != nil {
		return domain.ExecutionRecord{}, fmt.Errorf("audit execution requested: %w", err)
	}

	if err := s.audit(ctx, action.IncidentID, "execution_started", "completed", map[string]any{
		"candidate_action_id": action.ID,
		"execution_record_id": record.ID,
	}); err != nil {
		return domain.ExecutionRecord{}, fmt.Errorf("audit execution started: %w", err)
	}

	result, executionErr := s.executeWithRetry(ctx, action, AdapterRequest{
		IdempotencyKey: idempotencyKey,
		Timeout:        s.timeout,
		InitiatedBy:    initiatedBy,
	})
	record.ExecutorType = result.ExecutorType
	record.FinishedAt = s.now()
	executionDuration := record.FinishedAt.Sub(record.StartedAt)

	if executionErr != nil {
		record.Status = mapExecutionErrorToStatus(executionErr)
		if s.metrics != nil {
			s.metrics.ObserveExecution(record.Status, executionDuration)
		}
		record.ResultJSON = marshalExecutionPayload(map[string]any{
			"error": executionErr.Error(),
		})
		if err := s.repository.SaveExecutionRecord(ctx, record); err != nil {
			return domain.ExecutionRecord{}, fmt.Errorf("update failed execution record: %w", err)
		}
		if err := s.repository.UpdateCandidateActionStatus(ctx, action.ID, domain.CandidateActionStatusFailed); err != nil {
			return domain.ExecutionRecord{}, fmt.Errorf("update failed action status: %w", err)
		}
		if _, err := s.transitionIncidentState(ctx, incidentRecord, domain.IncidentStateFailedRemediation); err != nil {
			return domain.ExecutionRecord{}, fmt.Errorf("move incident to failed_remediation: %w", err)
		}
		if err := s.audit(ctx, action.IncidentID, "execution_failed", "completed", map[string]any{
			"candidate_action_id": action.ID,
			"execution_record_id": record.ID,
			"status":              record.Status,
			"error":               executionErr.Error(),
		}); err != nil {
			return domain.ExecutionRecord{}, fmt.Errorf("audit execution failure: %w", err)
		}
		return record, executionErr
	}

	record.Status = ExecutionStatusSucceeded
	if s.metrics != nil {
		s.metrics.ObserveExecution(record.Status, executionDuration)
	}
	record.ResultJSON = marshalExecutionPayload(result.Payload)
	if err := s.repository.SaveExecutionRecord(ctx, record); err != nil {
		return domain.ExecutionRecord{}, fmt.Errorf("update execution record: %w", err)
	}
	if err := s.repository.UpdateCandidateActionStatus(ctx, action.ID, domain.CandidateActionStatusSucceeded); err != nil {
		return domain.ExecutionRecord{}, fmt.Errorf("update action status to succeeded: %w", err)
	}
	if _, err := s.transitionIncidentState(ctx, incidentRecord, domain.IncidentStateVerifyingAction); err != nil {
		return domain.ExecutionRecord{}, fmt.Errorf("move incident to verifying_action: %w", err)
	}
	if err := s.audit(ctx, action.IncidentID, "execution_completed", "completed", map[string]any{
		"candidate_action_id": action.ID,
		"execution_record_id": record.ID,
		"status":              record.Status,
	}); err != nil {
		return domain.ExecutionRecord{}, fmt.Errorf("audit execution completed: %w", err)
	}
	if s.verifier != nil {
		if _, err := s.verifier.VerifyExecution(ctx, action, record); err != nil {
			return record, fmt.Errorf("verify execution outcome: %w", err)
		}
	}

	return record, nil
}

func (s *Service) executeWithRetry(ctx context.Context, action domain.CandidateAction, request AdapterRequest) (AdapterResult, error) {
	var result AdapterResult
	var err error
	for attempt := 0; attempt <= s.retries; attempt++ {
		executionCtx, cancel := context.WithTimeout(ctx, request.Timeout)
		result, err = s.adapter.Execute(executionCtx, action, request)
		cancel()
		if err == nil {
			return result, nil
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return result, err
		}

		var retryable RetryableError
		if !errors.As(err, &retryable) || attempt == s.retries {
			return result, err
		}
	}

	return result, err
}

func (s *Service) transitionIncidentState(ctx context.Context, incidentRecord domain.Incident, next domain.IncidentState) (domain.Incident, error) {
	if incidentRecord.State == next {
		return incidentRecord, nil
	}
	if !incident.CanTransition(incidentRecord.State, next) {
		return domain.Incident{}, fmt.Errorf("invalid incident state transition from %q to %q", incidentRecord.State, next)
	}
	if err := s.repository.UpdateIncidentState(ctx, incidentRecord.ID, next); err != nil {
		return domain.Incident{}, err
	}
	incidentRecord.State = next
	incidentRecord.UpdatedAt = s.now()
	return incidentRecord, nil
}

func (s *Service) audit(ctx context.Context, incidentID string, stepName string, status string, details map[string]any) error {
	body, err := json.Marshal(details)
	if err != nil {
		return err
	}
	return s.repository.AddAuditEvent(ctx, domain.AuditEvent{
		ID:          uuid.NewString(),
		IncidentID:  incidentID,
		StepName:    stepName,
		Status:      status,
		DetailsJSON: string(body),
		StartedAt:   s.now(),
		FinishedAt:  s.now(),
	})
}

func findDuplicateExecution(records []domain.ExecutionRecord, idempotencyKey string, cooldown time.Duration, now time.Time) *domain.ExecutionRecord {
	for idx := range records {
		record := records[idx]
		if record.IdempotencyKey != idempotencyKey {
			continue
		}
		if record.Status == ExecutionStatusStarted || record.Status == ExecutionStatusSucceeded {
			return &record
		}
		if !record.FinishedAt.IsZero() && now.Sub(record.FinishedAt) < cooldown {
			return &record
		}
	}
	return nil
}

func countExecutionAttempts(records []domain.ExecutionRecord) int {
	var attempts int
	for range records {
		attempts++
	}
	return attempts
}

func mapExecutionErrorToStatus(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return ExecutionStatusTimedOut
	}
	return ExecutionStatusFailed
}

func marshalExecutionPayload(payload map[string]any) string {
	if payload == nil {
		payload = map[string]any{}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "{}"
	}
	return string(body)
}

func validateExecutionScope(action domain.CandidateAction, incidentRecord domain.Incident, definition ActionDefinition) error {
	if len(definition.AllowedEnvironments) > 0 && !slices.Contains(definition.AllowedEnvironments, incidentRecord.Environment) {
		return fmt.Errorf("action %q is not allowed in environment %q", definition.Key, incidentRecord.Environment)
	}
	if len(definition.AllowedTargets) > 0 && !slices.Contains(definition.AllowedTargets, action.TargetResource) {
		return fmt.Errorf("target %q is not allowed for action %q", action.TargetResource, definition.Key)
	}
	return nil
}
