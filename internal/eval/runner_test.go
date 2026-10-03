package eval

import (
	"context"
	"errors"
	"testing"

	"github.com/Cyaside/Triovexa/internal/ai"
)

type scriptedClient struct {
	responses []ai.CompletionResult
	errs      []error
	calls     int
}

func (c *scriptedClient) CompleteJSON(ctx context.Context, messages []ai.ChatMessage) (string, error) {
	result, err := c.CompleteJSONDetailed(ctx, messages)
	return result.Content, err
}
func (c *scriptedClient) CompleteJSONDetailed(context.Context, []ai.ChatMessage) (ai.CompletionResult, error) {
	i := c.calls
	c.calls++
	if i >= len(c.responses) {
		return ai.CompletionResult{}, errors.New("script exhausted")
	}
	var err error
	if i < len(c.errs) {
		err = c.errs[i]
	}
	return c.responses[i], err
}

func TestProviderFailureRemainsFailureWithoutFallbackOrRemediation(t *testing.T) {
	client := &scriptedClient{responses: []ai.CompletionResult{{UsageStatus: ai.UsageKnown, Usage: ai.CompletionUsage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8}}}, errs: []error{errors.New("provider invalid content")}}
	report, err := Evaluate(t.Context(), []Case{{ID: "worker-stall", Service: "queue-worker", Environment: "staging", ExpectedAction: "restart_worker", Evidence: []Evidence{{Type: "metric", Snippet: "worker stalled"}}}}, Options{Mode: "fixture", Repeats: 1, Client: client})
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.FailedRuns != 1 || report.Summary.AccuracyPercent != 0 || report.Summary.KnownUsageTokens.TotalTokens != 8 || client.calls != 1 || report.Results[0].FailureStage != "triage" {
		t.Fatalf("failure/usage replaced: report=%+v calls=%d", report, client.calls)
	}
}

func TestInvalidPayloadRetainsUsageAndRemainingCasesContinue(t *testing.T) {
	client := &scriptedClient{responses: []ai.CompletionResult{
		{Content: "not-json", UsageStatus: ai.UsageKnown, Usage: ai.CompletionUsage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}},
		{Content: `{"summary":"Evidence inconclusive"}`, UsageStatus: ai.UsageMissing},
		{Content: `{"actions":[]}`, UsageStatus: ai.UsageMissing},
	}}
	report, err := Evaluate(t.Context(), []Case{{ID: "first"}, {ID: "second"}}, Options{Mode: "fixture", Repeats: 1, Client: client})
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.Runs != 2 || report.Summary.FailedRuns != 1 || report.Summary.AccuracyPercent != 50 || report.Summary.KnownUsageTokens.TotalTokens != 5 || report.Summary.UsageMissingCalls != 2 || client.calls != 3 {
		t.Fatalf("invalid failure denominator: %+v", report.Summary)
	}
}

func TestEvaluateRejectsDuplicateCaseBeforeCallingModel(t *testing.T) {
	client := &scriptedClient{}
	if _, err := Evaluate(t.Context(), []Case{{ID: "same"}, {ID: "same"}}, Options{Mode: "fixture", Repeats: 1, Client: client}); err == nil || client.calls != 0 {
		t.Fatal("duplicate case reached model")
	}
}
