package triage

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Cyaside/Triovexa/internal/domain"
)

type HeuristicGenerator struct{}

func NewHeuristicGenerator() *HeuristicGenerator {
	return &HeuristicGenerator{}
}

func (g *HeuristicGenerator) Generate(
	ctx context.Context,
	incident domain.Incident,
	evidence []domain.EvidenceItem,
	documents []domain.DocumentReference,
) (domain.TriageResult, error) {
	_ = ctx

	mode := detectMode(evidence)
	lastDeploy := extractMetadataField(evidence, "last_deploy")
	if lastDeploy == "" {
		lastDeploy = "unknown"
	}

	summary, hypotheses, blastRadius, nextSteps, confidence := summarizeIncident(incident, mode, lastDeploy, documents)

	return domain.TriageResult{
		ID:                uuid.NewString(),
		IncidentID:        incident.ID,
		Summary:           summary,
		Hypotheses:        hypotheses,
		BlastRadius:       blastRadius,
		NextSteps:         nextSteps,
		DraftStatusUpdate: fmt.Sprintf("[%s] %s", strings.ToUpper(incident.Severity), summary),
		ConfidenceNotes:   confidence,
		CreatedAt:         time.Now().UTC(),
	}, nil
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

func summarizeIncident(
	incident domain.Incident,
	mode string,
	lastDeploy string,
	documents []domain.DocumentReference,
) (string, []string, string, []string, string) {
	docHint := "No strongly relevant document was found."
	if len(documents) > 0 {
		docHint = fmt.Sprintf("Most relevant document: %s.", documents[0].DocumentTitle)
	}

	switch mode {
	case "worker_stall":
		return fmt.Sprintf("%s in %s has a growing job backlog and an unhealthy worker. %s", incident.ServiceName, incident.Environment, docHint),
			[]string{
				"the demo worker is stalled or crash-looping, leaving queued jobs unprocessed",
				"the latest deployment may have introduced a payload incompatibility in the worker",
			},
			"the primary blast radius is background processing, but synchronous requests may also degrade if the backlog continues to grow",
			[]string{
				"check worker health and restart patterns in the logs",
				"compare queue backlog before and after the alert",
				"review the worker restart or background job retry runbook",
			},
			"medium-to-high confidence because backlog, worker health, and retrieved documentation indicate a consistent failure pattern"
	case "timeout_after_deploy":
		return fmt.Sprintf("%s in %s is timing out after deployment %s, with a clear latency increase. %s", incident.ServiceName, incident.Environment, lastDeploy, docHint),
			[]string{
				"the latest deployment introduced a change that slowed a dependency or exhausted the internal timeout budget",
				"the error rate increased as a secondary effect of requests waiting too long",
			},
			"the primary request path is likely affected, especially operations that depend on the slow dependency",
			[]string{
				"confirm whether errors increased immediately after the latest deployment",
				"inspect timeout logs and identify the dependency that appears most often",
				"review the timeout-after-deploy postmortem and related rollback runbook",
			},
			"high confidence because deployment evidence and timeout patterns reinforce each other"
	default:
		return fmt.Sprintf("%s in %s shows error-rate and latency anomalies that require further triage. %s", incident.ServiceName, incident.Environment, docHint),
			[]string{
				"the primary request path is experiencing performance degradation",
				"the anomaly may be related to the latest deployment or an external dependency",
			},
			"users of the primary service and related background processes may be affected",
			[]string{
				"inspect the latest metric and log evidence",
				"compare the anomaly with the latest deployment context",
				"review runbooks or postmortems with matching keywords",
			},
			"medium confidence because the initial evidence is meaningful, but the incident pattern is not yet specific"
	}
}

func extractMetadataField(items []domain.EvidenceItem, key string) string {
	for _, item := range items {
		if item.MetadataJSON == "" {
			continue
		}

		var payload map[string]any
		if err := json.Unmarshal([]byte(item.MetadataJSON), &payload); err != nil {
			continue
		}

		if value, ok := payload[key]; ok {
			if typed, ok := value.(string); ok {
				return typed
			}
		}
	}

	return ""
}
