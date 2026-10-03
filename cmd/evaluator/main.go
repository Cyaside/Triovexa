package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Cyaside/Triovexa/internal/ai"
	"github.com/Cyaside/Triovexa/internal/eval"
)

func main() {
	var casesPath, outputPath, mode, provider, baseURL, model string
	var repeats int
	flag.StringVar(&casesPath, "cases", "evaluation/cases.json", "fixed evaluation case set")
	flag.StringVar(&outputPath, "out", "artifacts/evaluation/heuristic-baseline.json", "JSON report output")
	flag.StringVar(&mode, "mode", "heuristic", "heuristic or fixture; live evaluation is disabled")
	flag.StringVar(&provider, "provider", "openai-compatible", "fixture provider label")
	flag.StringVar(&baseURL, "base-url", "", "loopback fixture API root")
	flag.StringVar(&model, "model", "", "fixture model")
	flag.IntVar(&repeats, "repeats", 1, "runs per case")
	flag.Parse()
	if err := run(casesPath, outputPath, mode, provider, baseURL, model, repeats); err != nil {
		fmt.Fprintln(os.Stderr, "evaluation failed:", err)
		os.Exit(1)
	}
}

func run(casesPath, outputPath, mode, provider, baseURL, model string, repeats int) error {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "provider" {
		return errors.New("live evaluation is disabled: paid validation requires the shared admission campaign and final-smoke profile")
	}
	if mode != "heuristic" && mode != "fixture" {
		return errors.New("mode must be heuristic or fixture")
	}
	if repeats < 1 {
		return errors.New("repeats must be at least one")
	}
	options := eval.Options{Mode: mode, Provider: provider, Model: model, Repeats: repeats}
	if mode == "fixture" {
		if !isLoopback(baseURL) {
			return errors.New("fixture evaluation requires an HTTP loopback endpoint; external model dispatch is disabled")
		}
		client, err := ai.NewOpenAICompatibleClient(ai.ProviderConfig{Name: provider, BaseURL: baseURL, APIKey: "offline-fixture", Model: model, JSONMode: true, Timeout: 5 * time.Second, AllowHTTP: true})
		if err != nil {
			return err
		}
		if !client.Configured() {
			return errors.New("fixture mode requires a model")
		}
		options.Client = client
	} else {
		options.Provider, options.Model = "", ""
	}
	data, err := os.ReadFile(casesPath)
	if err != nil {
		return fmt.Errorf("read cases: %w", err)
	}
	var cases []eval.Case
	if err := json.Unmarshal(data, &cases); err != nil {
		return fmt.Errorf("decode cases: %w", err)
	}
	value, err := eval.Evaluate(context.Background(), cases, options)
	if err != nil {
		return err
	}
	if err := writeReport(outputPath, value); err != nil {
		return err
	}
	fmt.Printf("%s: %.1f%% exact accuracy across %d runs (%d failures), %d extra actions, %d invalid proposals\n", outputPath, value.Summary.AccuracyPercent, value.Summary.Runs, value.Summary.FailedRuns, value.Summary.ExtraActions, value.Summary.InvalidProposals)
	return nil
}

func writeReport(path string, value eval.Report) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0644); err != nil {
		return err
	}
	markdownPath := strings.TrimSuffix(path, filepath.Ext(path)) + ".md"
	summary := value.Summary
	markdown := fmt.Sprintf("# Reasoning evaluation\n\n- Mode: %s\n- Provider/model: %s / %s\n- Cases: %d\n- Runs: %d; completed: %d; failed: %d\n- Exact action accuracy: %.1f%% (all runs, including failures)\n- Citation validity: %.1f%% over %d runs with proposed actions\n- Extra actions: %d\n- Invalid proposals: %d\n- Average latency: %.1f ms\n- Usage calls: %d known / %d missing / %d invalid\n- Known usage only: %d prompt / %d completion / %d total tokens\n\nCitation validity checks reference membership, not semantic grounding. Semantic grounding is not assessed by this harness. No heuristic fallback contributes to a model score. Missing usage does not indicate zero spend. Cached/reasoning breakdowns are subsets of the reported totals. Heuristic and fixture results verify deterministic behavior; they do not estimate live-model accuracy or provider cost.\n", value.Mode, value.Provider, value.Model, summary.Cases, summary.Runs, summary.SuccessfulRuns, summary.FailedRuns, summary.AccuracyPercent, summary.CitationValidityPercent, summary.CitationValidityRuns, summary.ExtraActions, summary.InvalidProposals, summary.AverageLatencyMS, summary.UsageKnownCalls, summary.UsageMissingCalls, summary.UsageInvalidCalls, summary.KnownUsageTokens.PromptTokens, summary.KnownUsageTokens.CompletionTokens, summary.KnownUsageTokens.TotalTokens)
	return os.WriteFile(markdownPath, []byte(markdown), 0644)
}

func isLoopback(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}
