package modelgateway

import (
	"context"
	"errors"
	"fmt"

	"github.com/Cyaside/Triovexa/internal/ai"
	"github.com/Cyaside/Triovexa/internal/ai/admission"
)

type Summary struct {
	ModelRequests     int                `json:"model_requests"`
	InputTokenBound   int64              `json:"input_token_bound"`
	Usage             ai.CompletionUsage `json:"usage"`
	UsageStatus       string             `json:"usage_status"`
	ReservedMicroUSD  int64              `json:"reserved_micro_usd"`
	AccountedMicroUSD int64              `json:"accounted_micro_usd"`
	LatencyMS         int64              `json:"latency_ms"`
}

// ReadSummary reads ledger authority, never the child's claimed token counts.
// Accounted costs use the configured tariff; they are not provider invoices.
func ReadSummary(ctx context.Context, ledger *admission.Service, campaignID, attemptID, phase string, limit int) (Summary, error) {
	s := Summary{UsageStatus: "not_requested"}
	for ordinal := 1; ordinal <= limit; ordinal++ {
		r, err := ledger.GetRequest(ctx, fmt.Sprintf("%s:%s:%s:%d", campaignID, attemptID, phase, ordinal))
		if errors.Is(err, admission.ErrNotFound) {
			break
		}
		if err != nil {
			return s, err
		}
		if r.State == admission.Cancelled {
			continue
		}
		if r.State != admission.Reserved {
			s.ModelRequests++
			s.InputTokenBound += r.Request.InputTokenBound
		}
		if r.State != admission.Accounted {
			s.UsageStatus = "unknown"
			s.ReservedMicroUSD += r.ReservedMicroUSD
			continue
		}
		s.AccountedMicroUSD += r.ActualMicroUSD
		if s.UsageStatus != "unknown" {
			s.UsageStatus = "known"
		}
		if r.Receipt == nil || !r.Receipt.Usage.Present {
			s.UsageStatus = "unknown"
			continue
		}
		u := r.Receipt.Usage
		s.Usage.PromptTokens += int(u.PromptTokens)
		s.Usage.CompletionTokens += int(u.CompletionTokens)
		s.Usage.TotalTokens += int(u.TotalTokens)
		s.Usage.CachedPromptTokens += int(u.CachedInputTokens)
		s.Usage.ReasoningTokens += int(u.ReasoningTokens)
		s.LatencyMS += r.Receipt.LatencyMS
	}
	return s, nil
}
