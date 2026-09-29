package connections

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cyaside/Triovexa/internal/config"
	"github.com/Cyaside/Triovexa/internal/secretstore"
	"github.com/Cyaside/Triovexa/internal/storage/memory"
)

func TestLoadReasoningRestoresEncryptedCredential(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "credential.key")
	cipher, err := secretstore.NewCipher(keyPath, "")
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := cipher.Encrypt("stored-provider-secret")
	if err != nil {
		t.Fatal(err)
	}
	profile := config.ReasoningConnectionProfile{Provider: "openai-compatible",
		BaseURL: "https://example.invalid/v1", Model: "fixture-model", CredentialRef: "LLM_API_KEY", JSONMode: true}
	bundle, err := config.EncodeConnectionProfile(config.ReasoningConnectionBundle{
		Profile: profile, EncryptedAPIKey: sealed, EncryptionVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	settings := memory.NewMemoryStore()
	if err := settings.PutSetting(context.Background(), config.ReasoningConnectionBundleSettingKey, bundle); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{CredentialKeyPath: keyPath, LLMAPIKey: "stale-environment-secret"}
	if err := LoadReasoning(context.Background(), settings, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.LLMAPIKey != "stored-provider-secret" || cfg.LLMModel != "fixture-model" || cfg.LLMBaseURL != profile.BaseURL {
		t.Fatal("saved reasoning connection was not restored")
	}
	missing := filepath.Join(t.TempDir(), "missing.key")
	cfg.CredentialKeyPath = missing
	if err := LoadReasoning(context.Background(), settings, &cfg); err == nil {
		t.Fatal("saved connection accepted without its original encryption key")
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("replacement key was created after a saved credential could not be decrypted")
	}
}

func TestLoadReasoningRejectsInvalidSavedBundleWithoutEnvironmentFallback(t *testing.T) {
	settings := memory.NewMemoryStore()
	if err := settings.PutSetting(context.Background(), config.ReasoningConnectionBundleSettingKey, `{"broken":true}`); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{LLMAPIKey: "environment-secret"}
	if err := LoadReasoning(context.Background(), settings, &cfg); err == nil {
		t.Fatal("invalid saved bundle silently fell back to environment credential")
	}
	if cfg.LLMAPIKey != "environment-secret" {
		t.Fatal("failed load mutated configuration")
	}
}

func TestLoadReasoningUsesLegacyProfileOnlyWithoutBundle(t *testing.T) {
	t.Setenv("LEGACY_LLM_KEY", "legacy-secret")
	settings := memory.NewMemoryStore()
	raw, err := config.EncodeConnectionProfile(config.ReasoningConnectionProfile{
		Provider: "openai-compatible", BaseURL: "https://legacy.example/v1", Model: "legacy-model", CredentialRef: "LEGACY_LLM_KEY",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := settings.PutSetting(context.Background(), config.ReasoningConnectionSettingKey, raw); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{}
	if err := LoadReasoning(context.Background(), settings, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.LLMAPIKey != "legacy-secret" || cfg.LLMModel != "legacy-model" {
		t.Fatal("legacy profile was not applied")
	}
	if err := settings.PutSetting(context.Background(), config.ReasoningConnectionBundleSettingKey, `{"broken":true}`); err != nil {
		t.Fatal(err)
	}
	if err := LoadReasoning(context.Background(), settings, &cfg); err == nil {
		t.Fatal("invalid bundle incorrectly fell back to legacy profile")
	}
}

func TestLoadGrafanaRejectsInvalidSavedProfile(t *testing.T) {
	settings := memory.NewMemoryStore()
	if err := settings.PutSetting(context.Background(), config.GrafanaConnectionSettingKey, `{"base_url":"wrong"}`); err != nil {
		t.Fatal(err)
	}
	if err := LoadGrafana(context.Background(), settings, &config.Config{}); err == nil {
		t.Fatal("invalid saved Grafana connection was ignored")
	}
}

type failingSettings struct{}

func (failingSettings) GetSetting(context.Context, string) (string, error) {
	return "", errors.New("storage offline")
}

func TestLoadReasoningPropagatesStorageError(t *testing.T) {
	if err := LoadReasoning(context.Background(), failingSettings{}, &config.Config{}); err == nil || !strings.Contains(err.Error(), "storage offline") {
		t.Fatalf("storage error = %v", err)
	}
}
