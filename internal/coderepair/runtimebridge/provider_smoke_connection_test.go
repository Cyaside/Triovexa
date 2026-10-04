package runtimebridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/ai"
	"github.com/Cyaside/Triovexa/internal/ai/admission"
	"github.com/Cyaside/Triovexa/internal/config"
	"github.com/Cyaside/Triovexa/internal/connections"
	"github.com/Cyaside/Triovexa/internal/secretstore"
	"github.com/Cyaside/Triovexa/internal/storage"
)

type smokeSavedBundle string

func (bundle smokeSavedBundle) GetSetting(_ context.Context, key string) (string, error) {
	if key != config.ReasoningConnectionBundleSettingKey || bundle == "" {
		return "", storage.ErrNotFound
	}
	return string(bundle), nil
}

// Validate non-secret model/endpoint identity before the credential loader runs.
// Reuse LoadReasoning with a pinned bundle, so there is no environment/legacy
// selection if a saved UI credential is absent or has changed.
func smokeSavedProvider(ctx context.Context, raw string, pricing admission.Pricing, host string, loadKey func() (string, error)) (*ai.OpenAICompatibleClient, *secretstore.Cipher, error) {
	bundle, err := config.DecodeReasoningConnectionBundle(raw)
	if err != nil || bundle.Profile.Provider != pricing.Provider || bundle.Profile.Model != pricing.Model {
		return nil, nil, admission.ErrMismatch
	}
	endpoint, err := url.Parse(bundle.Profile.BaseURL)
	if err != nil || endpoint.Scheme != "https" || host == "" || !strings.EqualFold(endpoint.Hostname(), host) ||
		endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Port() != "" && endpoint.Port() != "443") {
		return nil, nil, errors.New("saved provider is outside the approved HTTPS host")
	}
	encoded, err := loadKey()
	if err != nil || strings.TrimSpace(encoded) == "" {
		return nil, nil, errors.New("existing credential encryption key is unavailable")
	}
	cipher, err := secretstore.NewCipher("", encoded)
	if err != nil {
		return nil, nil, errors.New("existing credential encryption key is invalid")
	}
	cfg := config.Config{CredentialEncryptionKey: encoded, LLMTimeout: 5 * time.Minute}
	if err := connections.LoadReasoning(ctx, smokeSavedBundle(raw), &cfg); err != nil {
		return nil, nil, errors.New("saved reasoning credential could not be opened")
	}
	client, err := ai.NewOpenAICompatibleClient(ai.ProviderConfig{Name: bundle.Profile.Provider, BaseURL: cfg.LLMBaseURL,
		Model: cfg.LLMModel, APIKey: cfg.LLMAPIKey, JSONMode: cfg.LLMJSONMode, Timeout: cfg.LLMTimeout,
		RequireAllowlist: true, AllowHosts: []string{host}})
	return client, cipher, err
}

func TestProviderSmokePinsSavedModelAndHostBeforeCredentialRead(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString(make([]byte, 32))
	cipher, _ := secretstore.NewCipher("", encoded)
	sealed, _ := cipher.Encrypt("synthetic-smoke-key")
	bundle := config.ReasoningConnectionBundle{Profile: config.ReasoningConnectionProfile{Provider: "openai-compatible", BaseURL: "https://fixture.example/v1",
		Model: "fixture", CredentialRef: "ENCRYPTED_LLM_API_KEY"}, EncryptedAPIKey: sealed, EncryptionVersion: 1}
	pricing := admission.Pricing{Provider: "openai-compatible", Model: "fixture"}
	for _, scenario := range []struct {
		name, host, model, endpoint string
		valid                       bool
	}{
		{"unchanged", "fixture.example", "fixture", "https://fixture.example/v1", true},
		{"model changed", "fixture.example", "different", "https://fixture.example/v1", false},
		{"host changed", "other.example", "fixture", "https://fixture.example/v1", false},
		{"host missing", "", "fixture", "https://fixture.example/v1", false},
		{"insecure endpoint", "fixture.example", "fixture", "http://fixture.example/v1", false},
		{"URL credential", "fixture.example", "fixture", "https://private:secret@fixture.example/v1", false},
		{"URL query", "fixture.example", "fixture", "https://fixture.example/v1?token=secret", false},
		{"URL fragment", "fixture.example", "fixture", "https://fixture.example/v1#secret", false},
		{"alternate port", "fixture.example", "fixture", "https://fixture.example:8443/v1", false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			candidate := bundle
			candidate.Profile.Model = scenario.model
			candidate.Profile.BaseURL = scenario.endpoint
			raw, _ := json.Marshal(candidate)
			reads := 0
			client, _, err := smokeSavedProvider(context.Background(), string(raw), pricing, scenario.host, func() (string, error) { reads++; return encoded, nil })
			if (err == nil) != scenario.valid || (reads != 0) != scenario.valid {
				t.Fatalf("saved selection: reads=%d err=%v", reads, err)
			}
			if scenario.valid && (client == nil || !client.Configured() || client.Model() != "fixture") {
				t.Fatal("saved credential was not retained")
			}
		})
	}
	if _, _, err := smokeSavedProvider(context.Background(), "", pricing, "fixture.example", func() (string, error) { t.Fatal("missing bundle read a credential"); return "", nil }); err == nil {
		t.Fatal("missing saved bundle selected an environment provider")
	}
}
