package ai

import (
	"bytes"
	"encoding/json"
)

// UsageStatus distinguishes an endpoint's token accounting from absent or
// inconsistent accounting. Missing usage never means a request was free.
type UsageStatus string

const (
	UsageKnown   UsageStatus = "known"
	UsageMissing UsageStatus = "missing"
	UsageInvalid UsageStatus = "invalid"
)

// ParseCompletionUsage preserves reported totals and optional breakdowns.
// Cached and reasoning counters are subsets, never added to TotalTokens.
func ParseCompletionUsage(raw json.RawMessage) (CompletionUsage, UsageStatus) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return CompletionUsage{}, UsageMissing
	}
	var wire struct {
		Prompt        *int `json:"prompt_tokens"`
		Completion    *int `json:"completion_tokens"`
		Total         *int `json:"total_tokens"`
		PromptDetails *struct {
			Cached *int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionDetails *struct {
			Reasoning *int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return CompletionUsage{}, UsageInvalid
	}
	var usage CompletionUsage
	if wire.Prompt != nil {
		usage.PromptTokens = *wire.Prompt
	}
	if wire.Completion != nil {
		usage.CompletionTokens = *wire.Completion
	}
	if wire.Total != nil {
		usage.TotalTokens = *wire.Total
	}
	if wire.PromptDetails != nil && wire.PromptDetails.Cached != nil {
		usage.CachedPromptTokens = *wire.PromptDetails.Cached
	}
	if wire.CompletionDetails != nil && wire.CompletionDetails.Reasoning != nil {
		usage.ReasoningTokens = *wire.CompletionDetails.Reasoning
	}
	if usage.PromptTokens < 0 || usage.CompletionTokens < 0 || usage.TotalTokens < 0 ||
		usage.CachedPromptTokens < 0 || usage.ReasoningTokens < 0 {
		return usage, UsageInvalid
	}
	if wire.Prompt == nil || wire.Completion == nil || wire.Total == nil {
		return usage, UsageMissing
	}
	if usage.TotalTokens < usage.PromptTokens || usage.TotalTokens-usage.PromptTokens != usage.CompletionTokens ||
		usage.CachedPromptTokens > usage.PromptTokens || usage.ReasoningTokens > usage.CompletionTokens {
		return usage, UsageInvalid
	}
	return usage, UsageKnown
}
