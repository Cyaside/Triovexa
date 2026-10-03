package ai

import (
	"encoding/json"
	"testing"
)

func TestUsageBreakdownsAreSubsetsNotAdditionalTokens(t *testing.T) {
	usage, status := ParseCompletionUsage(json.RawMessage(`{"prompt_tokens":10,"completion_tokens":8,"total_tokens":18,"prompt_tokens_details":{"cached_tokens":7},"completion_tokens_details":{"reasoning_tokens":6}}`))
	if status != UsageKnown || usage.TotalTokens != 18 || usage.CachedPromptTokens != 7 || usage.ReasoningTokens != 6 {
		t.Fatalf("usage=%+v status=%s", usage, status)
	}
}

func TestUsageRejectsNegativeInconsistentAndFractionalCounters(t *testing.T) {
	for _, raw := range []string{
		`{"prompt_tokens":-1,"completion_tokens":3,"total_tokens":2}`,
		`{"prompt_tokens":1,"completion_tokens":3,"total_tokens":9}`,
		`{"prompt_tokens":1.5,"completion_tokens":3,"total_tokens":4}`,
		`{"prompt_tokens":1,"completion_tokens":3,"total_tokens":4,"completion_tokens_details":{"reasoning_tokens":4}}`,
		`{"prompt_tokens":1,"completion_tokens":3,"total_tokens":4,"prompt_tokens_details":{"cached_tokens":2}}`,
	} {
		if _, status := ParseCompletionUsage(json.RawMessage(raw)); status != UsageInvalid {
			t.Fatalf("accepted usage %s: %s", raw, status)
		}
	}
}
