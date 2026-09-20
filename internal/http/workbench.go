package http

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/Cyaside/Triovexa/internal/alerting"
	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/incident"
	"github.com/Cyaside/Triovexa/internal/storage"
)

var errDemoScenarioNotFound = errors.New("demo scenario not found")

type demoScenarioDefinition struct {
	Key            string
	Name           string
	Title          string
	Summary        string
	SimulatePath   string
	FingerprintKey string
	ServiceName    string
	Environment    string
	Severity       string
	HeuristicFocus string
	ExpectedAction string
}

var demoScenarioCatalog = []demoScenarioDefinition{
	{
		Key:            "timeout-after-deploy",
		Name:           "Timeout After Deploy",
		Title:          "checkout timeout after deploy",
		Summary:        "Simulates a latency spike and request timeouts immediately after a deployment.",
		SimulatePath:   "/simulate/timeout-after-deploy",
		FingerprintKey: "ui-timeout-after-deploy",
		ServiceName:    "checkout-service",
		Environment:    "staging",
		Severity:       "critical",
		HeuristicFocus: "triage timeout, deploy correlation, cache refresh suggestion",
		ExpectedAction: "refresh_demo_cache",
	},
	{
		Key:            "worker-stall",
		Name:           "Worker Stall",
		Title:          "worker stall on checkout consumer",
		Summary:        "Simulates a growing worker backlog that eventually produces a medium-risk containment option.",
		SimulatePath:   "/simulate/worker-stall",
		FingerprintKey: "ui-worker-stall",
		ServiceName:    "checkout-service",
		Environment:    "staging",
		Severity:       "critical",
		HeuristicFocus: "worker recovery, backlog handling, rollback-aware medium-risk path",
		ExpectedAction: "restart_demo_worker + pause_demo_queue_consumer",
	},
	{
		Key:            "error-rate-spike",
		Name:           "Error Rate Spike",
		Title:          "checkout error rate spike",
		Summary:        "Simulates a high error rate on the primary path without taking down the entire service.",
		SimulatePath:   "/simulate/error-rate-spike",
		FingerprintKey: "ui-error-rate-spike",
		ServiceName:    "checkout-service",
		Environment:    "staging",
		Severity:       "critical",
		HeuristicFocus: "error-rate triage, cache refresh recommendation, fast verification loop",
		ExpectedAction: "refresh_demo_cache",
	},
}

func demoScenarioViews() []demoScenarioView {
	views := make([]demoScenarioView, 0, len(demoScenarioCatalog))
	for _, scenario := range demoScenarioCatalog {
		views = append(views, demoScenarioView{
			Key:            scenario.Key,
			Name:           scenario.Name,
			Summary:        scenario.Summary,
			HeuristicFocus: scenario.HeuristicFocus,
			ExpectedAction: scenario.ExpectedAction,
		})
	}

	return views
}

func triggerDemoScenario(ctx context.Context, demoBaseURL string, incidentService *incident.Service, scenarioKey string) (domain.Incident, error) {
	if incidentService == nil {
		return domain.Incident{}, errors.New("incident workflow is not configured")
	}

	scenario, ok := lookupDemoScenario(scenarioKey)
	if !ok {
		return domain.Incident{}, errDemoScenarioNotFound
	}

	if err := postDemoScenario(ctx, demoBaseURL, scenario.SimulatePath); err != nil {
		return domain.Incident{}, err
	}

	return incidentService.IngestGrafanaWebhookAsync(ctx, buildDemoScenarioWebhook(scenario))
}

func lookupDemoScenario(key string) (demoScenarioDefinition, bool) {
	for _, scenario := range demoScenarioCatalog {
		if scenario.Key == key {
			return scenario, true
		}
	}

	return demoScenarioDefinition{}, false
}

func postDemoScenario(ctx context.Context, baseURL string, path string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+path, nil)
	if err != nil {
		return fmt.Errorf("create demo scenario request: %w", err)
	}

	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		return fmt.Errorf("trigger demo scenario: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("demo scenario returned status %d", response.StatusCode)
	}

	return nil
}

func buildDemoScenarioWebhook(scenario demoScenarioDefinition) alerting.GrafanaWebhookPayload {
	startedAt := time.Now().UTC()

	return alerting.GrafanaWebhookPayload{
		Title: scenario.Title,
		CommonLabels: map[string]string{
			"service":     scenario.ServiceName,
			"environment": scenario.Environment,
			"severity":    scenario.Severity,
		},
		Alerts: []alerting.GrafanaAlert{
			{
				Status: "firing",
				Fingerprint: fmt.Sprintf(
					"%s-%d",
					scenario.FingerprintKey,
					startedAt.UnixNano(),
				),
				StartsAt: startedAt,
				Labels: map[string]string{
					"service":     scenario.ServiceName,
					"environment": scenario.Environment,
					"severity":    scenario.Severity,
				},
				Annotations: map[string]string{
					"summary": scenario.Summary,
				},
			},
		},
	}
}

func buildIncidentListItems(ctx context.Context, repository storage.Repository, incidents []domain.Incident) ([]incidentListItem, error) {
	sort.Slice(incidents, func(i, j int) bool {
		return incidents[i].UpdatedAt.After(incidents[j].UpdatedAt)
	})

	items := make([]incidentListItem, 0, len(incidents))
	for _, incidentRecord := range incidents {
		actions, err := repository.ListCandidateActions(ctx, incidentRecord.ID)
		if err != nil {
			return nil, err
		}

		var triageSummary string
		if triageResult, err := repository.GetTriageResult(ctx, incidentRecord.ID); err == nil {
			triageSummary = triageResult.Summary
		} else if !errors.Is(err, storage.ErrNotFound) {
			return nil, err
		}

		verificationResults, err := repository.ListVerificationResults(ctx, incidentRecord.ID)
		if err != nil {
			return nil, err
		}

		items = append(items, incidentListItem{
			Incident:                 incidentRecord,
			CandidateActionCount:     len(actions),
			PrimaryAction:            choosePrimaryAction(actions),
			TriageSummary:            triageSummary,
			LatestVerificationStatus: latestVerificationStatus(verificationResults),
		})
	}

	return items, nil
}

func buildIncidentDashboardStats(incidents []domain.Incident) incidentDashboardStats {
	stats := incidentDashboardStats{
		Total: len(incidents),
	}

	for _, incidentRecord := range incidents {
		switch incidentRecord.State {
		case domain.IncidentStateAwaitingApproval:
			stats.AwaitingApproval++
		case domain.IncidentStateExecutingAction, domain.IncidentStateVerifyingAction:
			stats.InFlight++
		case domain.IncidentStateResolved:
			stats.Resolved++
		case domain.IncidentStateRolledBack:
			stats.RolledBack++
		case domain.IncidentStateEscalated, domain.IncidentStateFailedRemediation:
			stats.Escalated++
		}
	}

	return stats
}

func choosePrimaryAction(actions []domain.CandidateAction) *domain.CandidateAction {
	if len(actions) == 0 {
		return nil
	}

	sort.Slice(actions, func(i, j int) bool {
		return actions[i].CreatedAt.Before(actions[j].CreatedAt)
	})

	for _, action := range actions {
		if action.Status != domain.CandidateActionStatusInvalid {
			copy := action
			return &copy
		}
	}

	copy := actions[0]
	return &copy
}

func latestVerificationStatus(results []domain.VerificationResult) string {
	if len(results) == 0 {
		return ""
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].CreatedAt.Before(results[j].CreatedAt)
	})

	return results[len(results)-1].Status
}

func describeNextOperatorStep(incidentRecord domain.Incident, killSwitchEnabled bool) string {
	if killSwitchEnabled {
		return "The kill switch is active. You can still inspect triage and evidence, but new approvals and executions remain blocked until it is disabled."
	}

	switch incidentRecord.State {
	case domain.IncidentStateAwaitingApproval:
		return "Review the heuristic rationale and policy decision, then approve or reject the safest candidate action."
	case domain.IncidentStateResolved:
		return "Verification succeeded. Review the before-and-after evidence and use the result for closure or a status update."
	case domain.IncidentStateRolledBack:
		return "The system completed an automatic rollback. Review the verification failure and rollback record before deciding whether to escalate."
	case domain.IncidentStateEscalated, domain.IncidentStateFailedRemediation:
		return "The heuristic result is insufficient for automatic closure. Hand the incident to an operator with its audit trail and collected evidence."
	case domain.IncidentStateExecutingAction, domain.IncidentStateVerifyingAction:
		return "Execution or verification is in progress. Monitor the action lane and do not submit duplicate actions before a final status appears."
	default:
		return "Use this page to review triage, inspect candidate actions, and follow the heuristic workflow end to end."
	}
}

func appendUIMessage(target string, key string, value string) string {
	if strings.TrimSpace(value) == "" {
		return target
	}

	parsed, err := url.Parse(target)
	if err != nil {
		return target
	}

	query := parsed.Query()
	query.Set(key, value)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func sanitizeUIRedirectTarget(raw string, fallback string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || !strings.HasPrefix(raw, "/ui/") {
		return fallback
	}

	return raw
}
