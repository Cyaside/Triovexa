package verification

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Cyaside/Triovexa/internal/demo"
	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/execution"
	"github.com/Cyaside/Triovexa/internal/incident"
	"github.com/Cyaside/Triovexa/internal/storage"
	"github.com/Cyaside/Triovexa/internal/telemetry"
)

const (
	StatusSuccess      = "success"
	StatusFailed       = "failed"
	StatusInconclusive = "inconclusive"
)

type SnapshotFetcher interface {
	Snapshot(context.Context) (demo.Snapshot, error)
}

type ContextualSnapshotFetcher interface {
	SnapshotForIncident(context.Context, domain.Incident) (demo.Snapshot, error)
}

type DemoSnapshotFetcher struct {
	baseURL string
	client  *http.Client
}

type WorkloadSnapshotFetcher struct {
	baseURL string
	token   string
	client  *http.Client
}

func NewWorkloadSnapshotFetcher(baseURL, token string) *WorkloadSnapshotFetcher {
	return &WorkloadSnapshotFetcher{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   strings.TrimSpace(token),
		client:  &http.Client{Timeout: 5 * time.Second},
	}
}

func (f *WorkloadSnapshotFetcher) Snapshot(ctx context.Context) (demo.Snapshot, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, f.baseURL+"/state", nil)
	if err != nil {
		return demo.Snapshot{}, err
	}
	request.Header.Set("Authorization", "Bearer "+f.token)
	response, err := f.client.Do(request)
	if err != nil {
		return demo.Snapshot{}, fmt.Errorf("request workload state: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode >= http.StatusBadRequest {
		return demo.Snapshot{}, fmt.Errorf("workload state returned %d", response.StatusCode)
	}
	var state struct {
		WorkerHealthy  bool      `json:"worker_healthy"`
		ConsumerPaused bool      `json:"consumer_paused"`
		QueueBacklog   int       `json:"queue_backlog"`
		Errors         int       `json:"errors"`
		Timestamp      time.Time `json:"timestamp"`
		Complete       bool      `json:"complete"`
	}
	if err := json.NewDecoder(response.Body).Decode(&state); err != nil {
		return demo.Snapshot{}, fmt.Errorf("decode workload state: %w", err)
	}
	if !state.Complete || state.Timestamp.IsZero() || time.Since(state.Timestamp) > time.Minute {
		return demo.Snapshot{}, fmt.Errorf("workload state is incomplete or stale")
	}
	mode := demo.ModeWorkerStall
	if state.WorkerHealthy && !state.ConsumerPaused {
		mode = demo.ModeHealthy
	}
	return demo.Snapshot{
		Mode:           mode,
		WorkerHealthy:  state.WorkerHealthy,
		ConsumerPaused: state.ConsumerPaused,
		QueueBacklog:   state.QueueBacklog,
		ErrorRate:      float64(state.Errors),
		LastUpdatedUTC: state.Timestamp,
	}, nil
}

func NewDemoSnapshotFetcher(baseURL string) *DemoSnapshotFetcher {
	return &DemoSnapshotFetcher{
		baseURL: strings.TrimRight(baseURL, "/"),
		client: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

func (f *DemoSnapshotFetcher) Snapshot(ctx context.Context) (demo.Snapshot, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, f.baseURL+"/state", nil)
	if err != nil {
		return demo.Snapshot{}, fmt.Errorf("create demo snapshot request: %w", err)
	}

	response, err := f.client.Do(request)
	if err != nil {
		return demo.Snapshot{}, fmt.Errorf("request demo snapshot: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode >= http.StatusBadRequest {
		return demo.Snapshot{}, fmt.Errorf("demo snapshot returned %d", response.StatusCode)
	}

	var snapshot demo.Snapshot
	if err := json.NewDecoder(response.Body).Decode(&snapshot); err != nil {
		return demo.Snapshot{}, fmt.Errorf("decode demo snapshot: %w", err)
	}

	return snapshot, nil
}

type Service struct {
	repository storage.Repository
	fetcher    SnapshotFetcher
	catalog    execution.Catalog
	rollbacker interface {
		RollbackAction(context.Context, domain.CandidateAction, string, string) (domain.RollbackRecord, error)
	}
	metrics *telemetry.Recorder
	now     func() time.Time

	observationInterval  time.Duration
	observationTimeout   time.Duration
	requiredObservations int
	baselineMaxAge       time.Duration
}

func NewService(
	repository storage.Repository,
	fetcher SnapshotFetcher,
	catalog execution.Catalog,
	rollbacker interface {
		RollbackAction(context.Context, domain.CandidateAction, string, string) (domain.RollbackRecord, error)
	},
) *Service {
	return &Service{
		repository: repository,
		fetcher:    fetcher,
		catalog:    catalog,
		rollbacker: rollbacker,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
}

func (s *Service) WithTelemetry(recorder *telemetry.Recorder) *Service {
	s.metrics = recorder
	return s
}

func (s *Service) WithRecoveryWindow(interval, timeout time.Duration, required int) *Service {
	if interval > 0 {
		s.observationInterval = interval
	}
	if timeout > 0 {
		s.observationTimeout = timeout
	}
	if required > 0 {
		s.requiredObservations = required
	}
	return s
}

func (s *Service) WithBaselineMaxAge(maxAge time.Duration) *Service {
	s.baselineMaxAge = maxAge
	return s
}

func (s *Service) VerifyExecution(ctx context.Context, action domain.CandidateAction, record domain.ExecutionRecord) (result domain.VerificationResult, err error) {
	startedAt := s.now()
	verificationStatus := ""
	defer func() {
		if s.metrics != nil {
			outcome := verificationStatus
			if err != nil {
				outcome = "error"
			}
			if outcome != "" {
				s.metrics.ObserveVerification(outcome, s.now().Sub(startedAt))
			}
		}
	}()
	if err := s.audit(ctx, action.IncidentID, "verification_started", "started", map[string]any{
		"candidate_action_id": action.ID,
		"execution_record_id": record.ID,
	}, startedAt, startedAt); err != nil {
		return domain.VerificationResult{}, fmt.Errorf("audit verification start: %w", err)
	}

	before, after, checks, status, notes, err := s.evaluate(ctx, action)
	if err != nil {
		return domain.VerificationResult{}, err
	}
	verificationStatus = status

	incidentRecord, err := s.repository.GetIncident(ctx, action.IncidentID)
	if err != nil {
		return domain.VerificationResult{}, fmt.Errorf("get incident for verification: %w", err)
	}

	evidenceJSON, err := marshalEvidence(map[string]any{
		"before":                 before,
		"after":                  after,
		"checks":                 checks,
		"draft_status_update":    s.buildDraftStatusUpdate(incidentRecord, action, record, status, checks),
		"escalation_recommended": status != StatusSuccess,
	})
	if err != nil {
		return domain.VerificationResult{}, fmt.Errorf("marshal verification evidence: %w", err)
	}

	result = domain.VerificationResult{
		ID:                uuid.NewString(),
		ExecutionRecordID: record.ID,
		Status:            status,
		EvidenceJSON:      evidenceJSON,
		Notes:             notes,
		CreatedAt:         s.now(),
	}
	if err := s.repository.SaveVerificationResult(ctx, result); err != nil {
		return domain.VerificationResult{}, fmt.Errorf("save verification result: %w", err)
	}

	switch status {
	case StatusSuccess:
		if _, err := s.transitionIncidentState(ctx, incidentRecord, domain.IncidentStateResolved); err != nil {
			return domain.VerificationResult{}, fmt.Errorf("move incident to resolved: %w", err)
		}
		if err := s.audit(ctx, action.IncidentID, "verification_succeeded", "completed", map[string]any{
			"candidate_action_id": action.ID,
			"execution_record_id": record.ID,
			"verification_id":     result.ID,
		}, startedAt, s.now()); err != nil {
			return domain.VerificationResult{}, fmt.Errorf("audit verification success: %w", err)
		}
	default:
		if err := s.repository.UpdateCandidateActionStatus(ctx, action.ID, domain.CandidateActionStatusFailed); err != nil {
			return domain.VerificationResult{}, fmt.Errorf("mark candidate action as failed: %w", err)
		}

		if status == StatusFailed {
			updatedIncident, err := s.transitionIncidentState(ctx, incidentRecord, domain.IncidentStateFailedRemediation)
			if err != nil {
				return domain.VerificationResult{}, fmt.Errorf("move incident to failed_remediation: %w", err)
			}
			incidentRecord = updatedIncident
			if err := s.audit(ctx, action.IncidentID, "verification_failed", "completed", map[string]any{
				"candidate_action_id": action.ID,
				"execution_record_id": record.ID,
				"verification_id":     result.ID,
			}, startedAt, s.now()); err != nil {
				return domain.VerificationResult{}, fmt.Errorf("audit verification failure: %w", err)
			}

			definition, ok := s.catalog.Get(action.ActionType)
			if ok && definition.SupportsRollback && definition.RollbackActionKey != "" && s.rollbacker != nil {
				if err := s.audit(ctx, action.IncidentID, "rollback_triggered", "started", map[string]any{
					"candidate_action_id": action.ID,
					"execution_record_id": record.ID,
					"rollback_action_key": definition.RollbackActionKey,
					"verification_id":     result.ID,
				}, startedAt, startedAt); err != nil {
					return domain.VerificationResult{}, fmt.Errorf("audit rollback start: %w", err)
				}

				rollbackRecord, rollbackErr := s.rollbacker.RollbackAction(ctx, action, "system:auto-rollback", "automatic rollback triggered after failed verification")
				if rollbackErr != nil {
					if err := s.updateVerificationResult(ctx, &result, map[string]any{
						"before":                 before,
						"after":                  after,
						"checks":                 checks,
						"draft_status_update":    fmt.Sprintf("Incident %q still requires escalation because verification failed and rollback %q also failed.", incidentRecord.Title, definition.RollbackActionKey),
						"escalation_recommended": true,
						"rollback_attempted":     true,
						"rollback_succeeded":     false,
						"rollback_action_key":    definition.RollbackActionKey,
					}, "verification failed and automatic rollback also failed; incident requires escalation"); err != nil {
						return domain.VerificationResult{}, err
					}
					if err := s.audit(ctx, action.IncidentID, "rollback_failed", "completed", map[string]any{
						"candidate_action_id": action.ID,
						"execution_record_id": record.ID,
						"verification_id":     result.ID,
						"rollback_action_key": definition.RollbackActionKey,
						"error":               rollbackErr.Error(),
					}, startedAt, s.now()); err != nil {
						return domain.VerificationResult{}, fmt.Errorf("audit rollback failure: %w", err)
					}
				} else {
					if err := s.updateVerificationResult(ctx, &result, map[string]any{
						"before":                 before,
						"after":                  after,
						"checks":                 checks,
						"draft_status_update":    fmt.Sprintf("Incident %q was safely rolled back after action %s degraded the service.", incidentRecord.Title, action.ActionType),
						"escalation_recommended": false,
						"rollback_attempted":     true,
						"rollback_succeeded":     true,
						"rollback_record_id":     rollbackRecord.ID,
						"rollback_action_key":    rollbackRecord.RollbackActionKey,
					}, "verification failed, but automatic rollback succeeded and incident was moved to rolled_back"); err != nil {
						return domain.VerificationResult{}, err
					}
					if _, err := s.transitionIncidentState(ctx, incidentRecord, domain.IncidentStateRolledBack); err != nil {
						return domain.VerificationResult{}, fmt.Errorf("move incident to rolled_back: %w", err)
					}
					if err := s.audit(ctx, action.IncidentID, "rollback_succeeded", "completed", map[string]any{
						"candidate_action_id": action.ID,
						"execution_record_id": record.ID,
						"verification_id":     result.ID,
						"rollback_record_id":  rollbackRecord.ID,
						"rollback_action_key": rollbackRecord.RollbackActionKey,
					}, startedAt, s.now()); err != nil {
						return domain.VerificationResult{}, fmt.Errorf("audit rollback success: %w", err)
					}
					return result, nil
				}
			}
		} else {
			if err := s.audit(ctx, action.IncidentID, "verification_inconclusive", "completed", map[string]any{
				"candidate_action_id": action.ID,
				"execution_record_id": record.ID,
				"verification_id":     result.ID,
			}, startedAt, s.now()); err != nil {
				return domain.VerificationResult{}, fmt.Errorf("audit inconclusive verification: %w", err)
			}
		}

		if _, err := s.transitionIncidentState(ctx, incidentRecord, domain.IncidentStateEscalated); err != nil {
			return domain.VerificationResult{}, fmt.Errorf("move incident to escalated: %w", err)
		}
		if err := s.audit(ctx, action.IncidentID, "escalation_triggered", "completed", map[string]any{
			"candidate_action_id": action.ID,
			"execution_record_id": record.ID,
			"verification_id":     result.ID,
			"verification_status": status,
			"reason":              notes,
		}, startedAt, s.now()); err != nil {
			return domain.VerificationResult{}, fmt.Errorf("audit escalation trigger: %w", err)
		}
	}

	return result, nil
}

func (s *Service) updateVerificationResult(ctx context.Context, result *domain.VerificationResult, evidence map[string]any, notes string) error {
	body, err := marshalEvidence(evidence)
	if err != nil {
		return fmt.Errorf("marshal verification result update: %w", err)
	}

	result.EvidenceJSON = body
	result.Notes = notes
	if err := s.repository.SaveVerificationResult(ctx, *result); err != nil {
		return fmt.Errorf("update verification result: %w", err)
	}

	return nil
}

func (s *Service) evaluate(ctx context.Context, action domain.CandidateAction) (demo.Snapshot, demo.Snapshot, map[string]bool, string, string, error) {
	incidentRecord, err := s.repository.GetIncident(ctx, action.IncidentID)
	if err != nil {
		return demo.Snapshot{}, demo.Snapshot{}, nil, "", "", fmt.Errorf("get incident for verification queries: %w", err)
	}
	evidence, err := s.repository.ListEvidenceItems(ctx, action.IncidentID)
	if err != nil {
		return demo.Snapshot{}, demo.Snapshot{}, nil, "", "", fmt.Errorf("list evidence for verification: %w", err)
	}

	before, baselineErr := deriveBaselineSnapshot(evidence)
	if baselineErr == nil && s.baselineMaxAge > 0 && (before.LastUpdatedUTC.IsZero() || s.now().Sub(before.LastUpdatedUTC) > s.baselineMaxAge) {
		baselineErr = fmt.Errorf("baseline evidence is stale")
	}
	after, afterErr := s.fetchAfterSnapshot(ctx, incidentRecord)

	checks := map[string]bool{}
	if baselineErr != nil || afterErr != nil {
		notes := "verification inconclusive: unable to collect before/after signal comparison"
		switch {
		case baselineErr != nil && afterErr != nil:
			notes = notes + fmt.Sprintf(" (%s; %s)", baselineErr.Error(), afterErr.Error())
		case baselineErr != nil:
			notes = notes + fmt.Sprintf(" (%s)", baselineErr.Error())
		case afterErr != nil:
			notes = notes + fmt.Sprintf(" (%s)", afterErr.Error())
		}
		return before, after, checks, StatusInconclusive, notes, nil
	}

	checks = buildChecks(before, after)
	if s.requiredObservations > 1 && recoveryChecksPassed(checks) {
		deadline := s.now().Add(s.observationTimeout)
		consecutive := 1
		for consecutive < s.requiredObservations && s.now().Before(deadline) {
			timer := time.NewTimer(s.observationInterval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return before, after, checks, StatusInconclusive, "verification cancelled before recovery was stable", nil
			case <-timer.C:
			}
			next, fetchErr := s.fetchAfterSnapshot(ctx, incidentRecord)
			if fetchErr != nil {
				return before, after, checks, StatusInconclusive, "verification inconclusive: recovery telemetry became unavailable", nil
			}
			after = next
			checks = buildChecks(before, after)
			if recoveryChecksPassed(checks) {
				consecutive++
			} else {
				consecutive = 0
			}
		}
		if consecutive < s.requiredObservations {
			return before, after, checks, StatusInconclusive, "verification inconclusive: recovery was not stable for the required observations", nil
		}
	}
	improvementCount := countTrue(
		checks["error_rate_improved"],
		checks["latency_improved"],
		checks["queue_backlog_improved"],
	)
	worsenedCount := countTrue(
		checks["error_rate_worsened"],
		checks["latency_worsened"],
		checks["queue_backlog_worsened"],
	)

	switch {
	case checks["alert_cleared"] && checks["health_check_normal"] && improvementCount >= 2 && worsenedCount == 0:
		return before, after, checks, StatusSuccess, "verification passed: alert cleared and core signals improved after execution", nil
	case !checks["alert_cleared"] || !checks["health_check_normal"] || worsenedCount >= 2:
		return before, after, checks, StatusFailed, "verification failed: service is still degraded or key signals worsened after execution", nil
	default:
		return before, after, checks, StatusInconclusive, "verification inconclusive: partial improvement observed but signals are not strong enough to auto-resolve", nil
	}
}

func recoveryChecksPassed(checks map[string]bool) bool {
	return checks["alert_cleared"] && checks["health_check_normal"] && checks["queue_backlog_improved"] && !checks["error_rate_worsened"]
}

func (s *Service) fetchAfterSnapshot(ctx context.Context, incidentRecord domain.Incident) (demo.Snapshot, error) {
	if s.fetcher == nil {
		return demo.Snapshot{}, fmt.Errorf("verification snapshot fetcher is not configured")
	}

	if contextual, ok := s.fetcher.(ContextualSnapshotFetcher); ok {
		return contextual.SnapshotForIncident(ctx, incidentRecord)
	}
	return s.fetcher.Snapshot(ctx)
}

func (s *Service) buildDraftStatusUpdate(
	incidentRecord domain.Incident,
	action domain.CandidateAction,
	record domain.ExecutionRecord,
	status string,
	checks map[string]bool,
) string {
	outcome := "needs human review"
	switch status {
	case StatusSuccess:
		outcome = "appears mitigated"
	case StatusFailed:
		outcome = "remains degraded and requires escalation"
	case StatusInconclusive:
		outcome = "shows mixed signals and requires escalation"
	}

	return fmt.Sprintf(
		"Incident %q for service %s %s after action %s executed by %s. Checks: alert_cleared=%t health_check_normal=%t error_rate_improved=%t latency_improved=%t queue_backlog_improved=%t.",
		incidentRecord.Title,
		incidentRecord.ServiceName,
		outcome,
		action.ActionType,
		record.InitiatedBy,
		checks["alert_cleared"],
		checks["health_check_normal"],
		checks["error_rate_improved"],
		checks["latency_improved"],
		checks["queue_backlog_improved"],
	)
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
	body, err := marshalEvidence(details)
	if err != nil {
		return err
	}

	return s.repository.AddAuditEvent(ctx, domain.AuditEvent{
		ID:          uuid.NewString(),
		IncidentID:  incidentID,
		StepName:    stepName,
		Status:      status,
		DetailsJSON: body,
		StartedAt:   startedAt,
		FinishedAt:  finishedAt,
	})
}

func deriveBaselineSnapshot(evidence []domain.EvidenceItem) (demo.Snapshot, error) {
	var snapshot demo.Snapshot
	var foundMetric bool
	var foundHealth bool

	for _, item := range evidence {
		if strings.TrimSpace(item.MetadataJSON) == "" {
			continue
		}

		var metadata map[string]any
		if err := json.Unmarshal([]byte(item.MetadataJSON), &metadata); err != nil {
			continue
		}

		if mode, ok := metadata["mode"].(string); ok && mode != "" {
			snapshot.Mode = demo.Mode(mode)
			snapshot.LastUpdatedUTC = item.Timestamp
		}
		if errorRate, ok := toFloat64(metadata["error_rate"]); ok {
			snapshot.ErrorRate = errorRate
			foundMetric = true
		}
		if latencyMs, ok := toInt(metadata["latency_ms"]); ok {
			snapshot.LatencyMs = latencyMs
			foundMetric = true
		}
		if queueBacklog, ok := toInt(metadata["queue_backlog"]); ok {
			snapshot.QueueBacklog = queueBacklog
			foundMetric = true
		}
		if replicaCount, ok := toInt(metadata["replica_count"]); ok {
			snapshot.ReplicaCount = replicaCount
		}
		if consumerPaused, ok := metadata["consumer_paused"].(bool); ok {
			snapshot.ConsumerPaused = consumerPaused
		}
		if workerHealthy, ok := metadata["workerHealthy"].(bool); ok {
			snapshot.WorkerHealthy = workerHealthy
			foundHealth = true
		}
		if lastDeploy, ok := metadata["last_deploy"].(string); ok {
			snapshot.LastDeploy = lastDeploy
		}
	}

	if !foundMetric {
		return demo.Snapshot{}, fmt.Errorf("baseline metrics are missing from evidence")
	}
	if !foundHealth {
		snapshot.WorkerHealthy = snapshot.Mode == demo.ModeHealthy
	}

	return snapshot, nil
}

func buildChecks(before demo.Snapshot, after demo.Snapshot) map[string]bool {
	return map[string]bool{
		"alert_cleared":          after.Mode == demo.ModeHealthy,
		"health_check_normal":    after.WorkerHealthy,
		"error_rate_improved":    after.ErrorRate < before.ErrorRate,
		"latency_improved":       after.LatencyMs < before.LatencyMs,
		"queue_backlog_improved": after.QueueBacklog < before.QueueBacklog,
		"error_rate_worsened":    after.ErrorRate > before.ErrorRate,
		"latency_worsened":       after.LatencyMs > before.LatencyMs,
		"queue_backlog_worsened": after.QueueBacklog > before.QueueBacklog,
	}
}

func countTrue(values ...bool) int {
	var count int
	for _, value := range values {
		if value {
			count++
		}
	}
	return count
}

func marshalEvidence(payload map[string]any) (string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	return string(body), nil
}

func toFloat64(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	default:
		return 0, false
	}
}

func toInt(value any) (int, bool) {
	switch typed := value.(type) {
	case float64:
		return int(typed), true
	case float32:
		return int(typed), true
	case int:
		return typed, true
	case int64:
		return int(typed), true
	default:
		return 0, false
	}
}
