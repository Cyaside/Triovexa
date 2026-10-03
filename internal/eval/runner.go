package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Cyaside/Triovexa/internal/ai"
	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/execution"
	"github.com/Cyaside/Triovexa/internal/remediation"
	"github.com/Cyaside/Triovexa/internal/security"
	"github.com/Cyaside/Triovexa/internal/triage"
)

type Options struct {
	Mode, Provider, Model string
	Repeats               int
	Client                ai.JSONCompleter
}

// Evaluate never switches algorithm after a failed generation. Provider errors
// are per-case results and remain in the accuracy denominator.
func Evaluate(ctx context.Context, cases []Case, options Options) (Report, error) {
	if options.Repeats < 1 || len(cases) == 0 {
		return Report{}, errors.New("evaluation requires cases and a positive repeat count")
	}
	if options.Mode != "heuristic" && options.Mode != "fixture" && options.Mode != "provider" {
		return Report{}, errors.New("unsupported evaluation mode")
	}
	if options.Mode != "heuristic" && options.Client == nil {
		return Report{}, errors.New("evaluation model is not configured")
	}
	seen := make(map[string]bool, len(cases))
	for _, item := range cases {
		if strings.TrimSpace(item.ID) == "" || seen[item.ID] {
			return Report{}, errors.New("case IDs must be non-empty and unique")
		}
		seen[item.ID] = true
	}
	catalog := execution.DefaultCatalog()
	tracker := &usageTracker{delegate: options.Client}
	results := make([]CaseResult, 0, len(cases)*options.Repeats)
	for repeat := 1; repeat <= options.Repeats; repeat++ {
		for _, item := range cases {
			if err := ctx.Err(); err != nil {
				return Report{}, err
			}
			incident, evidence, documents, err := ConvertCase(item)
			if err != nil {
				return Report{}, fmt.Errorf("case %s: %w", item.ID, err)
			}
			started := time.Now()
			tracker.observations = nil
			tracker.phase = "triage"
			var diagnosis domain.TriageResult
			if options.Mode == "heuristic" {
				diagnosis, err = triage.NewHeuristicGenerator().Generate(ctx, incident, evidence, documents)
			} else {
				diagnosis, err = triage.NewLLMGenerator(tracker).Generate(ctx, incident, evidence, documents)
			}
			stage := "triage"
			var actions []domain.CandidateAction
			if err == nil {
				stage = "remediation"
				tracker.phase = stage
				if options.Mode == "heuristic" {
					actions, err = remediation.NewHeuristicGenerator(catalog).Generate(ctx, incident, diagnosis, evidence, documents)
				} else {
					actions, err = remediation.NewLLMGenerator(catalog, tracker).Generate(ctx, incident, diagnosis, evidence, documents)
				}
			}
			result := Score(item, evidence, actions, repeat, time.Since(started))
			result.UsageObservations = append([]UsageObservation(nil), tracker.observations...)
			if err != nil {
				result.Accurate = false
				result.FailureStage = stage
				result.FailureReason = security.Redact(err.Error())
			}
			results = append(results, result)
		}
	}
	return Report{SchemaVersion: 2, GeneratedAt: time.Now().UTC(), Mode: options.Mode, Provider: options.Provider, Model: options.Model, PromptVersion: triage.PromptVersion + "/" + remediation.PromptVersion, Summary: Summarize(results, len(cases)), Results: results}, nil
}

func ConvertCase(item Case) (domain.Incident, []domain.EvidenceItem, []domain.DocumentReference, error) {
	now := time.Date(2026, time.September, 20, 12, 0, 0, 0, time.UTC)
	incident := domain.Incident{ID: item.ID, Title: item.Title, ServiceName: item.Service, Environment: item.Environment, Severity: item.Severity, State: domain.IncidentStateTriaging, CreatedAt: now, UpdatedAt: now}
	evidence := make([]domain.EvidenceItem, 0, len(item.Evidence))
	for i, source := range item.Evidence {
		metadata, err := json.Marshal(source.Metadata)
		if err != nil {
			return domain.Incident{}, nil, nil, err
		}
		evidence = append(evidence, domain.EvidenceItem{ID: fmt.Sprintf("%s-e%d", item.ID, i+1), IncidentID: item.ID, Type: source.Type, Source: source.Source, Snippet: source.Snippet, Timestamp: now, MetadataJSON: string(metadata)})
	}
	documents := make([]domain.DocumentReference, 0, len(item.Documents))
	for i, source := range item.Documents {
		documents = append(documents, domain.DocumentReference{ID: fmt.Sprintf("%s-d%d", item.ID, i+1), IncidentID: item.ID, DocumentTitle: source.Title, DocumentType: source.Type, RelevanceReason: source.Relevance, Snippet: source.Snippet})
	}
	return incident, evidence, documents, nil
}

type usageTracker struct {
	delegate     ai.JSONCompleter
	phase        string
	observations []UsageObservation
}

func (t *usageTracker) CompleteJSON(ctx context.Context, messages []ai.ChatMessage) (string, error) {
	result, err := t.CompleteJSONDetailed(ctx, messages)
	return result.Content, err
}
func (t *usageTracker) CompleteJSONDetailed(ctx context.Context, messages []ai.ChatMessage) (ai.CompletionResult, error) {
	var result ai.CompletionResult
	var err error
	if detailed, ok := t.delegate.(ai.DetailedJSONCompleter); ok {
		result, err = detailed.CompleteJSONDetailed(ctx, messages)
	} else {
		result.Content, err = t.delegate.CompleteJSON(ctx, messages)
	}
	status := result.UsageStatus
	if status == "" {
		status = ai.UsageMissing
	}
	t.observations = append(t.observations, UsageObservation{Phase: t.phase, Provider: result.Provider, Model: result.Model, Status: status, Usage: result.Usage})
	return result, err
}
