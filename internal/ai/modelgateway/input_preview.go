package modelgateway

import (
	"context"
	"errors"
)

// InputPreview measures the final SDK payload using the immutable input
// contract. It is not dispatch admission: campaign limits and receipts are
// checked atomically by DispatchAt, including after any concurrent spending.
type InputPreview struct {
	InputBound     int64 `json:"input_bound"`
	MaxInputTokens int64 `json:"max_input_tokens"`
}

// PreviewInput does not reserve tokens, create a request, or contact a provider.
// Oversized valid payloads return their measured bound so the runtime can
// replace completed tool content with immutable artifact references locally.
func (g *Gateway) PreviewInput(ctx context.Context, body []byte) (InputPreview, error) {
	if err := g.config.Fence(ctx); err != nil {
		return InputPreview{}, errors.New("LEASE_LOST")
	}
	bound, err := g.validateInput(body)
	if err != nil {
		return InputPreview{}, err
	}
	return InputPreview{InputBound: bound, MaxInputTokens: g.config.MaxInputTokens}, nil
}

func (g *Gateway) validateInput(body []byte) (int64, error) {
	if err := ValidatePayload(body, g.config.Provider.Model, g.config.MaxOutputTokens); err != nil {
		return 0, err
	}
	if err := validateStageTools(body, g.config.AllowedTools); err != nil {
		return 0, err
	}
	return inputTokenBound(g.config.Pricing, body)
}
