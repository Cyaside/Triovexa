package approval

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/incident"
	"github.com/Cyaside/Triovexa/internal/policy"
	"github.com/Cyaside/Triovexa/internal/storage"
	"github.com/Cyaside/Triovexa/internal/telemetry"
)

type KillSwitchState struct {
	Enabled   bool
	UpdatedAt time.Time
}

type KillSwitch struct {
	mu        sync.RWMutex
	enabled   bool
	updatedAt time.Time
}

func NewKillSwitch(initial bool) *KillSwitch {
	return &KillSwitch{
		enabled:   initial,
		updatedAt: time.Now().UTC(),
	}
}

func (k *KillSwitch) Enabled() bool {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.enabled
}

func (k *KillSwitch) Set(enabled bool) KillSwitchState {
	k.mu.Lock()
	defer k.mu.Unlock()

	k.enabled = enabled
	k.updatedAt = time.Now().UTC()
	return KillSwitchState{
		Enabled:   k.enabled,
		UpdatedAt: k.updatedAt,
	}
}

func (k *KillSwitch) Snapshot() KillSwitchState {
	k.mu.RLock()
	defer k.mu.RUnlock()

	return KillSwitchState{
		Enabled:   k.enabled,
		UpdatedAt: k.updatedAt,
	}
}

type Service struct {
	repository storage.Repository
	evaluator  policy.Evaluator
	killSwitch *KillSwitch
	metrics    *telemetry.Recorder
	now        func() time.Time
	settings   storage.SettingsStore
}

func NewService(repository storage.Repository, evaluator policy.Evaluator, killSwitch *KillSwitch) *Service {
	if killSwitch == nil {
		killSwitch = NewKillSwitch(false)
	}

	service := &Service{
		repository: repository,
		evaluator:  evaluator,
		killSwitch: killSwitch,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
	if settings, ok := repository.(storage.SettingsStore); ok {
		service.settings = settings
		if persisted, err := settings.GetSetting(context.Background(), "safety.kill_switch"); err == nil {
			if enabled, parseErr := strconv.ParseBool(persisted); parseErr == nil {
				service.killSwitch.Set(enabled)
			}
		}
	}
	return service
}

func (s *Service) WithTelemetry(recorder *telemetry.Recorder) *Service {
	s.metrics = recorder
	if s.metrics != nil {
		s.metrics.RecordKillSwitchState(s.killSwitch.Enabled())
	}
	return s
}

func (s *Service) EvaluateActions(ctx context.Context, incidentRecord domain.Incident, actions []domain.CandidateAction) (domain.Incident, error) {
	filtered := filterEvaluableActions(actions)
	if len(filtered) == 0 {
		return incidentRecord, nil
	}

	startedAt := s.now()
	if err := s.audit(ctx, incidentRecord.ID, "policy_evaluation", "started", map[string]any{
		"action_count": len(filtered),
	}, startedAt, startedAt); err != nil {
		return domain.Incident{}, fmt.Errorf("audit policy evaluation start: %w", err)
	}

	decisions := make([]domain.PolicyDecision, 0, len(filtered))
	var allowCount int
	var approvalCount int
	var denyCount int
	for _, action := range filtered {
		decision := s.evaluator.Evaluate(action, incidentRecord.Environment, s.killSwitch.Enabled())
		decision.ID = uuid.NewString()
		decisions = append(decisions, decision)
		if s.metrics != nil {
			s.metrics.IncPolicyDecision(string(decision.Decision))
		}

		nextStatus := statusFromDecision(decision)
		if err := s.repository.UpdateCandidateActionStatus(ctx, action.ID, nextStatus); err != nil {
			return domain.Incident{}, fmt.Errorf("update candidate action status: %w", err)
		}

		switch decision.Decision {
		case domain.PolicyDecisionAllow:
			allowCount++
		case domain.PolicyDecisionApprovalRequired:
			approvalCount++
		case domain.PolicyDecisionDeny:
			denyCount++
		}
	}

	if err := s.repository.SavePolicyDecisions(ctx, decisions); err != nil {
		return domain.Incident{}, fmt.Errorf("save policy decisions: %w", err)
	}

	if err := s.audit(ctx, incidentRecord.ID, "policy_evaluation", "completed", map[string]any{
		"allowed_count":           allowCount,
		"approval_required_count": approvalCount,
		"denied_count":            denyCount,
		"kill_switch_enabled":     s.killSwitch.Enabled(),
	}, startedAt, s.now()); err != nil {
		return domain.Incident{}, fmt.Errorf("audit policy evaluation completion: %w", err)
	}

	if denyCount > 0 {
		deniedAt := s.now()
		if err := s.audit(ctx, incidentRecord.ID, "policy_denied", "completed", map[string]any{
			"denied_count": denyCount,
		}, deniedAt, deniedAt); err != nil {
			return domain.Incident{}, fmt.Errorf("audit policy denied: %w", err)
		}
	}

	if approvalCount > 0 {
		requestedAt := s.now()
		if err := s.audit(ctx, incidentRecord.ID, "approval_requested", "completed", map[string]any{
			"approval_required_count": approvalCount,
		}, requestedAt, requestedAt); err != nil {
			return domain.Incident{}, fmt.Errorf("audit approval requested: %w", err)
		}
	}

	return s.refreshIncidentState(ctx, incidentRecord)
}

func (s *Service) ApproveAction(ctx context.Context, actionID string, approvedBy string, note string) (domain.CandidateAction, error) {
	if strings.TrimSpace(approvedBy) == "" {
		return domain.CandidateAction{}, fmt.Errorf("approved_by is required")
	}

	action, err := s.repository.GetCandidateAction(ctx, actionID)
	if err != nil {
		return domain.CandidateAction{}, fmt.Errorf("get candidate action: %w", err)
	}

	if action.Status != domain.CandidateActionStatusAwaitingApproval {
		return domain.CandidateAction{}, fmt.Errorf("candidate action %q is not awaiting approval", actionID)
	}

	decision, err := s.repository.GetPolicyDecision(ctx, actionID)
	if err != nil {
		return domain.CandidateAction{}, fmt.Errorf("get policy decision: %w", err)
	}
	now := s.now()
	record := domain.ApprovalRecord{
		ID:                uuid.NewString(),
		CandidateActionID: actionID,
		ApprovedBy:        approvedBy,
		Decision:          "approved",
		Note:              note,
		ActionDigest:      domain.ActionApprovalDigest(action, decision.PolicyRuleRef),
		PolicyVersion:     decision.PolicyRuleRef,
		ExpiresAt:         now.Add(15 * time.Minute),
		CreatedAt:         now,
	}
	if err := s.repository.CreateApprovalRecord(ctx, record); err != nil {
		return domain.CandidateAction{}, fmt.Errorf("create approval record: %w", err)
	}

	if err := s.repository.UpdateCandidateActionStatus(ctx, actionID, domain.CandidateActionStatusApproved); err != nil {
		return domain.CandidateAction{}, fmt.Errorf("update action status: %w", err)
	}

	incidentRecord, err := s.repository.GetIncident(ctx, action.IncidentID)
	if err != nil {
		return domain.CandidateAction{}, fmt.Errorf("get incident for action: %w", err)
	}

	incidentRecord, err = s.transitionIncidentState(ctx, incidentRecord, domain.IncidentStateApproved)
	if err != nil {
		return domain.CandidateAction{}, fmt.Errorf("move incident to approved: %w", err)
	}

	auditedAt := s.now()
	if err := s.audit(ctx, incidentRecord.ID, "action_approved", "completed", map[string]any{
		"candidate_action_id": actionID,
		"approved_by":         approvedBy,
	}, auditedAt, auditedAt); err != nil {
		return domain.CandidateAction{}, fmt.Errorf("audit action approved: %w", err)
	}

	return s.repository.GetCandidateAction(ctx, actionID)
}

func (s *Service) RejectAction(ctx context.Context, actionID string, approvedBy string, note string) (domain.CandidateAction, error) {
	if strings.TrimSpace(approvedBy) == "" {
		return domain.CandidateAction{}, fmt.Errorf("approved_by is required")
	}

	action, err := s.repository.GetCandidateAction(ctx, actionID)
	if err != nil {
		return domain.CandidateAction{}, fmt.Errorf("get candidate action: %w", err)
	}

	if action.Status != domain.CandidateActionStatusAwaitingApproval {
		return domain.CandidateAction{}, fmt.Errorf("candidate action %q is not awaiting approval", actionID)
	}

	record := domain.ApprovalRecord{
		ID:                uuid.NewString(),
		CandidateActionID: actionID,
		ApprovedBy:        approvedBy,
		Decision:          "rejected",
		Note:              note,
		CreatedAt:         s.now(),
	}
	if err := s.repository.CreateApprovalRecord(ctx, record); err != nil {
		return domain.CandidateAction{}, fmt.Errorf("create approval record: %w", err)
	}

	if err := s.repository.UpdateCandidateActionStatus(ctx, actionID, domain.CandidateActionStatusDenied); err != nil {
		return domain.CandidateAction{}, fmt.Errorf("update action status: %w", err)
	}

	incidentRecord, err := s.repository.GetIncident(ctx, action.IncidentID)
	if err != nil {
		return domain.CandidateAction{}, fmt.Errorf("get incident for action: %w", err)
	}

	if _, err := s.refreshIncidentState(ctx, incidentRecord); err != nil {
		return domain.CandidateAction{}, fmt.Errorf("refresh incident state: %w", err)
	}

	auditedAt := s.now()
	if err := s.audit(ctx, action.IncidentID, "action_rejected", "completed", map[string]any{
		"candidate_action_id": actionID,
		"approved_by":         approvedBy,
	}, auditedAt, auditedAt); err != nil {
		return domain.CandidateAction{}, fmt.Errorf("audit action rejected: %w", err)
	}

	return s.repository.GetCandidateAction(ctx, actionID)
}

func (s *Service) SetKillSwitch(enabled bool) KillSwitchState {
	state := s.killSwitch.Set(enabled)
	if s.settings != nil {
		if err := s.settings.PutSetting(context.Background(), "safety.kill_switch", strconv.FormatBool(enabled)); err != nil {
			state = s.killSwitch.Set(true)
		}
	}
	if s.metrics != nil {
		s.metrics.RecordKillSwitchState(state.Enabled)
	}
	return state
}

func (s *Service) KillSwitchState() KillSwitchState {
	return s.killSwitch.Snapshot()
}

func (s *Service) refreshIncidentState(ctx context.Context, incidentRecord domain.Incident) (domain.Incident, error) {
	actions, err := s.repository.ListCandidateActions(ctx, incidentRecord.ID)
	if err != nil {
		return domain.Incident{}, fmt.Errorf("list candidate actions: %w", err)
	}

	nextState := domain.IncidentStateActionProposed
	for _, action := range actions {
		if action.Status == domain.CandidateActionStatusApproved || action.Status == domain.CandidateActionStatusAllowed {
			nextState = domain.IncidentStateApproved
			break
		}
		if action.Status == domain.CandidateActionStatusAwaitingApproval {
			nextState = domain.IncidentStateAwaitingApproval
		}
	}

	return s.transitionIncidentState(ctx, incidentRecord, nextState)
}

func (s *Service) transitionIncidentState(ctx context.Context, incidentRecord domain.Incident, next domain.IncidentState) (domain.Incident, error) {
	if incidentRecord.State == next {
		return incidentRecord, nil
	}

	if !incident.CanTransition(incidentRecord.State, next) {
		return domain.Incident{}, fmt.Errorf("invalid incident state transition from %q to %q", incidentRecord.State, next)
	}

	if conditional, ok := s.repository.(storage.ConditionalStateStore); ok {
		updated, err := conditional.CompareAndSwapIncidentState(ctx, incidentRecord.ID, incidentRecord.State, next)
		if err != nil {
			return domain.Incident{}, err
		}
		if !updated {
			return domain.Incident{}, fmt.Errorf("incident state changed while transitioning from %q to %q", incidentRecord.State, next)
		}
	} else if err := s.repository.UpdateIncidentState(ctx, incidentRecord.ID, next); err != nil {
		return domain.Incident{}, err
	}

	incidentRecord.State = next
	incidentRecord.UpdatedAt = s.now()
	return incidentRecord, nil
}

func (s *Service) audit(
	ctx context.Context,
	incidentID string,
	stepName string,
	status string,
	details map[string]any,
	startedAt time.Time,
	finishedAt time.Time,
) error {
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
		StartedAt:   startedAt,
		FinishedAt:  finishedAt,
	})
}

func filterEvaluableActions(actions []domain.CandidateAction) []domain.CandidateAction {
	filtered := make([]domain.CandidateAction, 0, len(actions))
	for _, action := range actions {
		if action.Status != domain.CandidateActionStatusInvalid {
			filtered = append(filtered, action)
		}
	}

	return filtered
}

func statusFromDecision(decision domain.PolicyDecision) domain.CandidateActionStatus {
	switch decision.Decision {
	case domain.PolicyDecisionAllow:
		return domain.CandidateActionStatusAllowed
	case domain.PolicyDecisionApprovalRequired:
		return domain.CandidateActionStatusAwaitingApproval
	default:
		return domain.CandidateActionStatusDenied
	}
}
