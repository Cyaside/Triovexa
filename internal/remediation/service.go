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
		action, err := g.newAction(incident, "restart_demo_worker", "demo-worker", map[string]any{
			"worker_id": "worker-primary",
		}, []string{metricEvidence, logEvidence}, fmt.Sprintf("Backlog tinggi dan worker tidak sehat membuat restart worker demo menjadi kandidat aksi teraman. Ringkasan triage: %s", triage.Summary))
		if err != nil {
			return nil, err
		}
		actions = append(actions, g.validateAction(incident, action))

		if queueBacklog >= 80 {
			retryAction, err := g.newAction(incident, "retry_demo_background_job", "demo-job-runner", map[string]any{
				"job_id": "backlog-drain-batch",
			}, []string{metricEvidence, logEvidence}, "Backlog yang sudah menumpuk layak diikuti dengan retry batch job aman untuk membantu drain antrean setelah worker kembali sehat.")
			if err != nil {
				return nil, err
			}
			actions = append(actions, g.validateAction(incident, retryAction))
		}
	case "timeout_after_deploy":
		action, err := g.newAction(incident, "refresh_demo_cache", "demo-cache", map[string]any{
			"cache_key": "checkout-session",
		}, []string{metricEvidence, deployEvidence}, fmt.Sprintf("Timeout setelah deploy %s mengindikasikan cache non-critical bisa stale dan layak direfresh sebagai aksi low-risk pertama.", extractDeployVersion(evidence)))
		if err != nil {
			return nil, err
		}
		actions = append(actions, g.validateAction(incident, action))
	case "error_rate_spike":
		if latencyMs >= 500 {
			action, err := g.newAction(incident, "refresh_demo_cache", "demo-cache", map[string]any{
				"cache_key": "hot-path",
			}, []string{metricEvidence, logEvidence}, "Lonjakan error dan latency pada jalur utama masih cocok dengan cache refresh non-critical sebagai langkah aman untuk mengurangi tekanan awal.")
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
		action.ApprovalHint = "validation failed: action tidak ada di catalog"
		return action
	}

	if action.RiskLevel != definition.RiskLevel {
		action.Status = domain.CandidateActionStatusInvalid
		action.ApprovalHint = "validation failed: risk level tidak sesuai catalog"
		return action
	}

	if len(definition.AllowedEnvironments) > 0 && !slices.Contains(definition.AllowedEnvironments, incident.Environment) {
		action.Status = domain.CandidateActionStatusInvalid
		action.ApprovalHint = fmt.Sprintf("validation failed: environment %q tidak diizinkan untuk action ini", incident.Environment)
		return action
	}

	if len(definition.AllowedTargets) > 0 && !slices.Contains(definition.AllowedTargets, action.TargetResource) {
		action.Status = domain.CandidateActionStatusInvalid
		action.ApprovalHint = fmt.Sprintf("validation failed: target %q tidak diizinkan", action.TargetResource)
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
		return "Action ini valid tetapi tetap membutuhkan approval operator sebelum dieksekusi."
	}

	return "Action ini tidak memerlukan approval tambahan."
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
