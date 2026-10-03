package modelgateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"

	"github.com/Cyaside/Triovexa/internal/ai"
	"github.com/Cyaside/Triovexa/internal/ai/admission"
	"github.com/Cyaside/Triovexa/internal/secretstore"
)

type BudgetConfig struct {
	Campaign             admission.Campaign `json:"campaign"`
	Pricing              admission.Pricing  `json:"pricing"`
	ConfigVersion        string             `json:"config_version"`
	MaxInputTokens       int64              `json:"max_input_tokens"`
	MaxOutputTokens      int                `json:"max_output_tokens"`
	RepairCandidateLimit int                `json:"repair_candidate_limit,omitempty"`
}

func LoadBudgetConfig(path string) (BudgetConfig, error) {
	if path == "" {
		return BudgetConfig{}, admission.ErrPricingUnknown
	}
	file, err := os.Open(path)
	if err != nil {
		return BudgetConfig{}, errors.New("model admission configuration could not be opened")
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 16*1024))
	decoder.DisallowUnknownFields()
	var config BudgetConfig
	if decoder.Decode(&config) != nil || decoder.Decode(new(any)) != io.EOF {
		return BudgetConfig{}, errors.New("model admission configuration is invalid")
	}
	if err := config.Validate(); err != nil {
		return BudgetConfig{}, err
	}
	return config, nil
}

func (c BudgetConfig) Validate() error {
	if c.RepairCandidateLimit < 0 || c.RepairCandidateLimit > 3 || (c.Campaign.Profile != "internal" && c.RepairCandidateLimit > 1) {
		return admission.ErrInvalid
	}
	if c.ConfigVersion == "" || c.MaxInputTokens <= 0 || c.MaxOutputTokens <= 0 || c.Campaign.MaxRequests > 100 || c.Campaign.MaxRequests < 1 {
		return admission.ErrInvalid
	}
	if c.Campaign.Profile == "final-smoke" {
		if c.Campaign.Offline || c.Campaign.MaxSpendMicroUSD > 200000 || c.Campaign.MaxRequests > 6 || c.Campaign.MaxInputTokens > 12000 || c.MaxInputTokens > 6000 || c.MaxOutputTokens > 1500 {
			return admission.ErrInvalid
		}
	} else if c.Campaign.Profile == "offline-fixture" {
		if !c.Campaign.Offline {
			return admission.ErrInvalid
		}
	} else if c.Campaign.Profile != "internal" {
		return admission.ErrInvalid
	}
	return nil
}

type SharedDispatcher struct {
	config BudgetConfig
	ledger *admission.Service
	cipher *secretstore.Cipher
}

func NewSharedDispatcher(ctx context.Context, config BudgetConfig, ledger *admission.Service, cipher *secretstore.Cipher) (*SharedDispatcher, error) {
	if config.Validate() != nil || ledger == nil || cipher == nil {
		return nil, admission.ErrInvalid
	}
	if err := ledger.CreateCampaign(ctx, config.Campaign); err != nil {
		return nil, err
	}
	return &SharedDispatcher{config: config, ledger: ledger, cipher: cipher}, nil
}

// Dispatch is shared by triage, remediation and connection checks. Each
// workflow supplies a stable scope and the persistent campaign never resets.
func (d *SharedDispatcher) Dispatch(ctx context.Context, provider ai.ProviderConfig, body []byte) ([]byte, int, time.Duration, error) {
	scope, ok := ai.RequestScopeFrom(ctx)
	if !ok {
		return nil, 0, 0, errors.New("model call requires a stable workflow request scope")
	}
	gateway, err := New(Config{Provider: provider, Pricing: d.config.Pricing, CampaignID: d.config.Campaign.ID,
		AttemptID: scope.RunID, Phase: scope.Phase, ConfigVersion: d.config.ConfigVersion,
		MaxInputTokens: d.config.MaxInputTokens, MaxOutputTokens: d.config.MaxOutputTokens,
		MaxRequests: int(d.config.Campaign.MaxRequests), Cipher: d.cipher, Fence: func(ctx context.Context) error { return ctx.Err() }}, d.ledger)
	if err != nil {
		return nil, 0, 0, err
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal(body, &payload) != nil {
		return nil, 0, 0, errors.New("model request is invalid")
	}
	if _, exists := payload["max_tokens"]; !exists {
		payload["max_tokens"], _ = json.Marshal(d.config.MaxOutputTokens)
	}
	bounded, _ := json.Marshal(payload)
	return gateway.DispatchAt(ctx, scope.Ordinal, bounded)
}

type BlockedDispatcher struct{ Reason error }

func (d BlockedDispatcher) Dispatch(context.Context, ai.ProviderConfig, []byte) ([]byte, int, time.Duration, error) {
	if d.Reason == nil {
		return nil, 0, 0, admission.ErrPricingUnknown
	}
	return nil, 0, 0, d.Reason
}
