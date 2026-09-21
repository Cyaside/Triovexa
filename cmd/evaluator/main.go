package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Cyaside/Triovexa/internal/ai"
	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/execution"
	"github.com/Cyaside/Triovexa/internal/remediation"
	"github.com/Cyaside/Triovexa/internal/triage"
)

type evaluationCase struct {
	ID             string               `json:"id"`
	Title          string               `json:"title"`
	Service        string               `json:"service"`
	Environment    string               `json:"environment"`
	Severity       string               `json:"severity"`
	Evidence       []evaluationEvidence `json:"evidence"`
	Documents      []evaluationDocument `json:"documents"`
	ExpectedAction string               `json:"expected_action"`
}

type evaluationEvidence struct {
	Type     string         `json:"type"`
	Source   string         `json:"source"`
	Snippet  string         `json:"snippet"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type evaluationDocument struct {
	Title     string `json:"title"`
	Type      string `json:"type"`
	Relevance string `json:"relevance"`
	Snippet   string `json:"snippet,omitempty"`
}

type caseResult struct {
	CaseID              string   `json:"case_id"`
	Repeat              int      `json:"repeat"`
	ExpectedAction      string   `json:"expected_action,omitempty"`
	ProposedActions     []string `json:"proposed_actions"`
	Accurate            bool     `json:"accurate"`
	EvidenceGrounded    bool     `json:"evidence_grounded"`
	InvalidProposals    int      `json:"invalid_proposals"`
	Fallback            bool     `json:"fallback"`
	FallbackReason      string   `json:"fallback_reason,omitempty"`
	LatencyMilliseconds int64    `json:"latency_ms"`
	PromptTokens        int      `json:"prompt_tokens,omitempty"`
	CompletionTokens    int      `json:"completion_tokens,omitempty"`
	TotalTokens         int      `json:"total_tokens,omitempty"`
}

type summary struct {
	Runs                     int     `json:"runs"`
	Cases                    int     `json:"cases"`
	AccuracyPercent          float64 `json:"accuracy_percent"`
	EvidenceGroundingPercent float64 `json:"evidence_grounding_percent"`
	InvalidProposals         int     `json:"invalid_proposals"`
	Fallbacks                int     `json:"fallbacks"`
	AverageLatencyMS         float64 `json:"average_latency_ms"`
	PromptTokens             int     `json:"prompt_tokens"`
	CompletionTokens         int     `json:"completion_tokens"`
	TotalTokens              int     `json:"total_tokens"`
}

type report struct {
	SchemaVersion int          `json:"schema_version"`
	GeneratedAt   time.Time    `json:"generated_at"`
	Mode          string       `json:"mode"`
	Provider      string       `json:"provider,omitempty"`
	Model         string       `json:"model,omitempty"`
	PromptVersion string       `json:"prompt_version"`
	Summary       summary      `json:"summary"`
	Results       []caseResult `json:"results"`
}

type usageTracker struct {
	delegate ai.JSONCompleter
	usage    ai.CompletionUsage
}

func (t *usageTracker) CompleteJSON(ctx context.Context, messages []ai.ChatMessage) (string, error) {
	if detailed, ok := t.delegate.(ai.DetailedJSONCompleter); ok {
		result, err := detailed.CompleteJSONDetailed(ctx, messages)
		t.add(result.Usage)
		return result.Content, err
	}
	return t.delegate.CompleteJSON(ctx, messages)
}

func (t *usageTracker) CompleteJSONDetailed(ctx context.Context, messages []ai.ChatMessage) (ai.CompletionResult, error) {
	if detailed, ok := t.delegate.(ai.DetailedJSONCompleter); ok {
		result, err := detailed.CompleteJSONDetailed(ctx, messages)
		t.add(result.Usage)
		return result, err
	}
	content, err := t.delegate.CompleteJSON(ctx, messages)
	return ai.CompletionResult{Content: content}, err
}

func (t *usageTracker) add(value ai.CompletionUsage) {
	t.usage.PromptTokens += value.PromptTokens
	t.usage.CompletionTokens += value.CompletionTokens
	t.usage.TotalTokens += value.TotalTokens
}

func (t *usageTracker) reset() ai.CompletionUsage {
	value := t.usage
	t.usage = ai.CompletionUsage{}
	return value
}

func main() {
	var casesPath string
	var outputPath string
	var mode string
	var provider string
	var baseURL string
	var model string
	var apiKeyEnv string
	var repeats int
	flag.StringVar(&casesPath, "cases", "evaluation/cases.json", "path to the fixed evaluation cases")
	flag.StringVar(&outputPath, "out", "evaluation/results/heuristic-baseline.json", "JSON report output path")
	flag.StringVar(&mode, "mode", "heuristic", "heuristic or provider")
	flag.StringVar(&provider, "provider", "openai-compatible", "provider label")
	flag.StringVar(&baseURL, "base-url", "", "provider API root")
	flag.StringVar(&model, "model", "", "provider model")
	flag.StringVar(&apiKeyEnv, "api-key-env", "TRIOVEXA_EVAL_API_KEY", "environment variable containing the provider credential")
	flag.IntVar(&repeats, "repeats", 1, "number of runs per case")
	flag.Parse()

	if err := run(casesPath, outputPath, mode, provider, baseURL, model, apiKeyEnv, repeats); err != nil {
		fmt.Fprintln(os.Stderr, "evaluation failed:", err)
		os.Exit(1)
	}
}

func run(casesPath, outputPath, mode, provider, baseURL, model, apiKeyEnv string, repeats int) error {
	if repeats < 1 {
		return errors.New("repeats must be at least one")
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != "heuristic" && mode != "provider" {
		return errors.New("mode must be heuristic or provider")
	}

	cases, err := loadCases(casesPath)
	if err != nil {
		return err
	}
	if len(cases) != 30 {
		return fmt.Errorf("expected exactly 30 fixed cases, got %d", len(cases))
	}

	var tracker *usageTracker
	if mode == "provider" {
		client, clientErr := ai.NewOpenAICompatibleClient(ai.ProviderConfig{
			Name: provider, BaseURL: baseURL, APIKey: os.Getenv(apiKeyEnv), Model: model,
			JSONMode: true, Timeout: 30 * time.Second, AllowHTTP: isLoopback(baseURL),
		})
		if clientErr != nil {
			return clientErr
		}
		if !client.Configured() {
			return fmt.Errorf("provider mode requires base URL, model, and credential in %s", apiKeyEnv)
		}
		tracker = &usageTracker{delegate: client}
	}

	catalog := execution.DefaultCatalog()
	heuristicTriage := triage.NewHeuristicGenerator()
	heuristicActions := remediation.NewHeuristicGenerator(catalog)
	results := make([]caseResult, 0, len(cases)*repeats)

	for repeat := 1; repeat <= repeats; repeat++ {
		for _, item := range cases {
			incident, evidence, documents, convertErr := convertCase(item)
			if convertErr != nil {
				return fmt.Errorf("case %s: %w", item.ID, convertErr)
			}
			startedAt := time.Now()
			fallback := false
			var fallbackReasons []string

			triageResult, generateErr := heuristicTriage.Generate(context.Background(), incident, evidence, documents)
			if mode == "provider" {
				triageResult, generateErr = triage.NewLLMGenerator(tracker).Generate(context.Background(), incident, evidence, documents)
				if generateErr != nil {
					fallback = true
					fallbackReasons = append(fallbackReasons, "triage: "+generateErr.Error())
					triageResult, generateErr = heuristicTriage.Generate(context.Background(), incident, evidence, documents)
				}
			}
			if generateErr != nil {
				return fmt.Errorf("case %s triage: %w", item.ID, generateErr)
			}

			actions, generateErr := heuristicActions.Generate(context.Background(), incident, triageResult, evidence, documents)
			if mode == "provider" {
				actions, generateErr = remediation.NewLLMGenerator(catalog, tracker).Generate(context.Background(), incident, triageResult, evidence, documents)
				if generateErr != nil {
					fallback = true
					fallbackReasons = append(fallbackReasons, "remediation: "+generateErr.Error())
					actions, generateErr = heuristicActions.Generate(context.Background(), incident, triageResult, evidence, documents)
				}
			}
			if generateErr != nil {
				return fmt.Errorf("case %s remediation: %w", item.ID, generateErr)
			}

			usage := ai.CompletionUsage{}
			if tracker != nil {
				usage = tracker.reset()
			}
			results = append(results, score(item, evidence, actions, repeat, time.Since(startedAt), fallback, strings.Join(fallbackReasons, "; "), usage))
		}
	}

	reportValue := report{
		SchemaVersion: 1,
		GeneratedAt:   time.Now().UTC(),
		Mode:          mode,
		PromptVersion: "triage-v1/remediation-v1",
		Summary:       summarize(results, len(cases)),
		Results:       results,
	}
	if mode == "provider" {
		reportValue.Provider = provider
		reportValue.Model = model
	}
	if err := writeReport(outputPath, reportValue); err != nil {
		return err
	}
	fmt.Printf("%s: %.1f%% accuracy, %.1f%% grounded, %d invalid, %d fallbacks\n", outputPath, reportValue.Summary.AccuracyPercent, reportValue.Summary.EvidenceGroundingPercent, reportValue.Summary.InvalidProposals, reportValue.Summary.Fallbacks)
	return nil
}

func loadCases(path string) ([]evaluationCase, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read cases: %w", err)
	}
	var cases []evaluationCase
	if err := json.Unmarshal(data, &cases); err != nil {
		return nil, fmt.Errorf("decode cases: %w", err)
	}
	seen := map[string]bool{}
	for _, item := range cases {
		if strings.TrimSpace(item.ID) == "" || seen[item.ID] {
			return nil, fmt.Errorf("case IDs must be unique and non-empty: %q", item.ID)
		}
		seen[item.ID] = true
	}
	return cases, nil
}

func convertCase(item evaluationCase) (domain.Incident, []domain.EvidenceItem, []domain.DocumentReference, error) {
	now := time.Date(2026, time.September, 20, 12, 0, 0, 0, time.UTC)
	incident := domain.Incident{ID: item.ID, Title: item.Title, ServiceName: item.Service, Environment: item.Environment, Severity: item.Severity, State: domain.IncidentStateTriaging, CreatedAt: now, UpdatedAt: now}
	evidence := make([]domain.EvidenceItem, 0, len(item.Evidence))
	for index, source := range item.Evidence {
		metadata, err := json.Marshal(source.Metadata)
		if err != nil {
			return domain.Incident{}, nil, nil, err
		}
		evidence = append(evidence, domain.EvidenceItem{ID: fmt.Sprintf("%s-e%d", item.ID, index+1), IncidentID: item.ID, Type: source.Type, Source: source.Source, Snippet: source.Snippet, Timestamp: now, MetadataJSON: string(metadata)})
	}
	documents := make([]domain.DocumentReference, 0, len(item.Documents))
	for index, source := range item.Documents {
		documents = append(documents, domain.DocumentReference{ID: fmt.Sprintf("%s-d%d", item.ID, index+1), IncidentID: item.ID, DocumentTitle: source.Title, DocumentType: source.Type, RelevanceReason: source.Relevance, Snippet: source.Snippet})
	}
	return incident, evidence, documents, nil
}

func score(item evaluationCase, evidence []domain.EvidenceItem, actions []domain.CandidateAction, repeat int, latency time.Duration, fallback bool, fallbackReason string, usage ai.CompletionUsage) caseResult {
	validEvidence := map[string]bool{}
	for _, item := range evidence {
		validEvidence[item.ID] = true
	}
	grounded := true
	proposed := make([]string, 0, len(actions))
	invalid := 0
	for _, action := range actions {
		proposed = append(proposed, action.ActionType)
		if action.Status == domain.CandidateActionStatusInvalid {
			invalid++
		}
		if len(action.EvidenceRefs) == 0 && action.ActionType != "" {
			grounded = false
		}
		for _, evidenceID := range action.EvidenceRefs {
			if !validEvidence[evidenceID] {
				grounded = false
			}
		}
	}
	sort.Strings(proposed)
	accurate := false
	if item.ExpectedAction == "" {
		accurate = len(actions) == 0
	} else {
		for _, action := range actions {
			if action.ActionType == item.ExpectedAction && action.Status != domain.CandidateActionStatusInvalid {
				accurate = true
				break
			}
		}
	}
	return caseResult{CaseID: item.ID, Repeat: repeat, ExpectedAction: item.ExpectedAction, ProposedActions: proposed, Accurate: accurate, EvidenceGrounded: grounded, InvalidProposals: invalid, Fallback: fallback, FallbackReason: fallbackReason, LatencyMilliseconds: latency.Milliseconds(), PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens, TotalTokens: usage.TotalTokens}
}

func summarize(results []caseResult, cases int) summary {
	value := summary{Runs: len(results), Cases: cases}
	var accurate, grounded int
	var totalLatency int64
	for _, result := range results {
		if result.Accurate {
			accurate++
		}
		if result.EvidenceGrounded {
			grounded++
		}
		value.InvalidProposals += result.InvalidProposals
		if result.Fallback {
			value.Fallbacks++
		}
		totalLatency += result.LatencyMilliseconds
		value.PromptTokens += result.PromptTokens
		value.CompletionTokens += result.CompletionTokens
		value.TotalTokens += result.TotalTokens
	}
	if len(results) > 0 {
		value.AccuracyPercent = float64(accurate) * 100 / float64(len(results))
		value.EvidenceGroundingPercent = float64(grounded) * 100 / float64(len(results))
		value.AverageLatencyMS = float64(totalLatency) / float64(len(results))
	}
	return value
}

func writeReport(path string, value report) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return err
	}
	markdownPath := strings.TrimSuffix(path, filepath.Ext(path)) + ".md"
	markdown := fmt.Sprintf("# Evaluation report\n\n- Mode: `%s`\n- Provider/model: `%s` / `%s`\n- Fixed cases: %d\n- Runs: %d\n- Accuracy: %.1f%%\n- Evidence grounding: %.1f%%\n- Invalid proposals: %d\n- Fallbacks: %d\n- Average latency: %.1f ms\n- Tokens: %d prompt / %d completion / %d total\n\nThe JSON report beside this file contains per-case outcomes. Provider results are comparable only when run with the same case set, repeat count, and prompt version.\n", value.Mode, value.Provider, value.Model, value.Summary.Cases, value.Summary.Runs, value.Summary.AccuracyPercent, value.Summary.EvidenceGroundingPercent, value.Summary.InvalidProposals, value.Summary.Fallbacks, value.Summary.AverageLatencyMS, value.Summary.PromptTokens, value.Summary.CompletionTokens, value.Summary.TotalTokens)
	return os.WriteFile(markdownPath, []byte(markdown), 0o644)
}

func isLoopback(rawURL string) bool {
	lower := strings.ToLower(strings.TrimSpace(rawURL))
	return strings.HasPrefix(lower, "http://localhost") || strings.HasPrefix(lower, "http://127.0.0.1") || strings.HasPrefix(lower, "http://[::1]")
}
