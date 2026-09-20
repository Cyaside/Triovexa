package remediation

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/execution"
)

type HeuristicGenerator struct {
	catalog execution.Catalog
}

func NewHeuristicGenerator(catalog execution.Catalog) *HeuristicGenerator {
	return &HeuristicGenerator{catalog: catalog}
}

func (g *HeuristicGenerator) CatalogMode() string {
	return "heuristic-constrained"
}

func (g *HeuristicGenerator) Generate(
	ctx context.Context,
	incident domain.Incident,
	triage domain.TriageResult,
	evidence []domain.EvidenceItem,
	documents []domain.DocumentReference,
) ([]domain.CandidateAction, error) {
	_ = ctx
	_ = documents

	var actions []domain.CandidateAction
	mode := detectMode(evidence)
	metricEvidence := findEvidenceByType(evidence, "metric")
	logEvidence := findEvidenceByType(evidence, "log")
	deployEvidence := findEvidenceByType(evidence, "deploy")
	queueBacklog := extractIntMetric(evidence, "queue_backlog")
	latencyMs := extractIntMetric(evidence, "latency_ms")

	switch mode {
	case "worker_stall":
		actionType := "restart_demo_worker"
		target := "demo-worker"
		workerID := "worker-primary"
		rationale := fmt.Sprintf("A large backlog and unhealthy worker make a demo worker restart the safest candidate action. Triage summary: %s", triage.Summary)
		realWorkload := incident.ServiceName == "queue-worker"
		if realWorkload {
			actionType = "restart_worker"
			target = "queue-worker"
			workerID = "queue-worker"
			rationale = fmt.Sprintf("The Redis Streams backlog and unhealthy consumer make an allowlisted worker restart the safest candidate action. Triage summary: %s", triage.Summary)
		}
		action, err := g.newAction(incident, actionType, target, map[string]any{
			"worker_id": workerID,
		}, []string{metricEvidence, logEvidence}, rationale)
		if err != nil {
			return nil, err
		}
		actions = append(actions, g.validateAction(incident, action))

		if queueBacklog >= 80 && !realWorkload {
			retryAction, err := g.newAction(incident, "retry_demo_background_job", "demo-job-runner", map[string]any{
				"job_id": "backlog-drain-batch",
			}, []string{metricEvidence, logEvidence}, "The accumulated backlog warrants a bounded batch-job retry to help drain the queue after the worker becomes healthy.")
			if err != nil {
				return nil, err
			}
			actions = append(actions, g.validateAction(incident, retryAction))
		}

		if queueBacklog >= 120 {
			pauseActionType := "pause_demo_queue_consumer"
			pauseTarget := "demo-queue-consumer"
			if realWorkload {
				pauseActionType = "pause_consumer"
				pauseTarget = "queue-worker"
			}
			pauseAction, err := g.newAction(incident, pauseActionType, pauseTarget, map[string]any{}, []string{metricEvidence, logEvidence}, "The extreme backlog makes pausing the queue consumer a medium-risk containment option while the operator investigates downstream pressure.")
			if err != nil {
				return nil, err
			}
			actions = append(actions, g.validateAction(incident, pauseAction))
		}
	case "timeout_after_deploy":
		action, err := g.newAction(incident, "refresh_demo_cache", "demo-cache", map[string]any{
			"cache_key": "checkout-session",
		}, []string{metricEvidence, deployEvidence}, fmt.Sprintf("Timeouts after deployment %s indicate that a non-critical cache may be stale, making a refresh a reasonable first low-risk action.", extractDeployVersion(evidence)))
		if err != nil {
			return nil, err
		}
		actions = append(actions, g.validateAction(incident, action))
	case "error_rate_spike":
		if latencyMs >= 500 {
			action, err := g.newAction(incident, "refresh_demo_cache", "demo-cache", map[string]any{
				"cache_key": "hot-path",
			}, []string{metricEvidence, logEvidence}, "The error and latency spike on the primary path is consistent with a stale non-critical cache, so a refresh is a safe first step to reduce pressure.")
			if err != nil {
				return nil, err
			}
			actions = append(actions, g.validateAction(incident, action))
		}
	}

	return actions, nil
}

func (g *HeuristicGenerator) newAction(
	incident domain.Incident,
	actionType string,
	target string,
	parameters map[string]any,
	evidenceRefs []string,
	rationale string,
) (domain.CandidateAction, error) {
	definition, ok := g.catalog.Get(actionType)
	if !ok {
		return domain.CandidateAction{}, fmt.Errorf("action %q not found in catalog", actionType)
	}

	payload, err := json.Marshal(parameters)
	if err != nil {
		return domain.CandidateAction{}, fmt.Errorf("marshal action parameters: %w", err)
	}

	return domain.CandidateAction{
		ID:             uuid.NewString(),
		IncidentID:     incident.ID,
		ActionType:     actionType,
		TargetResource: target,
		ParametersJSON: string(payload),
		RiskLevel:      definition.RiskLevel,
		Rationale:      rationale,
		EvidenceRefs:   compactStrings(evidenceRefs),
		ApprovalHint:   approvalHint(definition),
		Status:         domain.CandidateActionStatusProposed,
		CreatedAt:      time.Now().UTC(),
	}, nil
}

func (g *HeuristicGenerator) validateAction(incident domain.Incident, action domain.CandidateAction) domain.CandidateAction {
	definition, ok := g.catalog.Get(action.ActionType)
	if !ok {
		action.Status = domain.CandidateActionStatusInvalid
		action.ApprovalHint = "validation failed: action is not present in the catalog"
		return action
	}

	if action.RiskLevel != definition.RiskLevel {
		action.Status = domain.CandidateActionStatusInvalid
		action.ApprovalHint = "validation failed: risk level does not match the catalog"
		return action
	}

	if len(definition.AllowedEnvironments) > 0 && !slices.Contains(definition.AllowedEnvironments, incident.Environment) {
		action.Status = domain.CandidateActionStatusInvalid
		action.ApprovalHint = fmt.Sprintf("validation failed: environment %q is not allowed for this action", incident.Environment)
		return action
	}

	if len(definition.AllowedTargets) > 0 && !slices.Contains(definition.AllowedTargets, action.TargetResource) {
		action.Status = domain.CandidateActionStatusInvalid
		action.ApprovalHint = fmt.Sprintf("validation failed: target %q is not allowed", action.TargetResource)
		return action
	}

	if err := validateParameters(definition, action.ParametersJSON); err != nil {
		action.Status = domain.CandidateActionStatusInvalid
		action.ApprovalHint = fmt.Sprintf("validation failed: %s", err.Error())
		return action
	}

	action.ApprovalHint = approvalHint(definition)
	return action
}

func validateParameters(definition execution.ActionDefinition, parametersJSON string) error {
	if strings.TrimSpace(parametersJSON) == "" {
		parametersJSON = "{}"
	}

	var parameters map[string]any
	if err := json.Unmarshal([]byte(parametersJSON), &parameters); err != nil {
		return fmt.Errorf("parameters_json is not valid JSON object")
	}

	allowedParameters := make(map[string]execution.ParameterDefinition, len(definition.Parameters))
	for _, parameter := range definition.Parameters {
		allowedParameters[parameter.Name] = parameter
	}

	for key := range parameters {
		if _, ok := allowedParameters[key]; !ok {
			return fmt.Errorf("parameter %q is not defined in catalog", key)
		}
	}

	for _, parameter := range definition.Parameters {
		if parameter.Required {
			if _, ok := parameters[parameter.Name]; !ok {
				return fmt.Errorf("required parameter %q is missing", parameter.Name)
			}
		}
	}

	return nil
}

func approvalHint(definition execution.ActionDefinition) string {
	if definition.ApprovalRequired {
		if definition.SupportsRollback && definition.RollbackActionKey != "" {
			return fmt.Sprintf("This action is valid but requires operator approval before execution. Prepared rollback: %s.", definition.RollbackActionKey)
		}
		return "This action is valid but requires operator approval before execution."
	}

	return "This action does not require additional approval."
}

func detectMode(evidence []domain.EvidenceItem) string {
	for _, item := range evidence {
		snippet := strings.ToLower(item.Snippet)
		switch {
		case strings.Contains(snippet, "worker stalled"):
			return "worker_stall"
		case strings.Contains(snippet, "request timeouts increased"):
			return "timeout_after_deploy"
		case strings.Contains(snippet, "error rate spike"):
			return "error_rate_spike"
		}
	}

	return "unknown"
}

func findEvidenceByType(evidence []domain.EvidenceItem, itemType string) string {
	for _, item := range evidence {
		if item.Type == itemType {
			return item.ID
		}
	}

	return ""
}

func extractDeployVersion(evidence []domain.EvidenceItem) string {
	for _, item := range evidence {
		if item.Type != "deploy" {
			continue
		}

		if version := extractStringField(item.MetadataJSON, "last_deploy"); version != "" {
			return version
		}
	}

	return "unknown"
}

func extractIntMetric(evidence []domain.EvidenceItem, key string) int {
	for _, item := range evidence {
		if item.Type != "metric" {
			continue
		}

		var payload map[string]any
		if err := json.Unmarshal([]byte(item.MetadataJSON), &payload); err != nil {
			continue
		}

		value, ok := payload[key]
		if !ok {
			continue
		}

		switch typed := value.(type) {
		case float64:
			return int(typed)
		case int:
			return typed
		}
	}

	return 0
}

func extractStringField(body string, key string) string {
	if strings.TrimSpace(body) == "" {
		return ""
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return ""
	}

	value, ok := payload[key]
	if !ok {
		return ""
	}

	typed, ok := value.(string)
	if !ok {
		return ""
	}

	return typed
}

func compactStrings(values []string) []string {
	filtered := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			filtered = append(filtered, value)
		}
	}

	return filtered
}
