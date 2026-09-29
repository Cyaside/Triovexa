package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	appconfig "github.com/Cyaside/Triovexa/internal/config"
	"github.com/Cyaside/Triovexa/internal/secretstore"
	"github.com/Cyaside/Triovexa/internal/storage"
)

type settingsFixture map[string]string

func (s settingsFixture) GetSetting(_ context.Context, key string) (string, error) {
	if value, exists := s[key]; exists {
		return value, nil
	}
	return "", storage.ErrNotFound
}

func TestLoadReasoningConnectionUsesEncryptedStoredCredential(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "credential.key")
	cipher, err := secretstore.NewCipher(keyPath, "")
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := cipher.Encrypt("stored-provider-secret")
	if err != nil {
		t.Fatal(err)
	}
	profile := appconfig.ReasoningConnectionProfile{Provider: "openai-compatible",
		BaseURL: "https://example.invalid/v1", Model: "fixture-model", CredentialRef: "LLM_API_KEY", JSONMode: true}
	bundle, err := appconfig.EncodeConnectionProfile(appconfig.ReasoningConnectionBundle{
		Profile: profile, EncryptedAPIKey: sealed, EncryptionVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	settings := settingsFixture{appconfig.ReasoningConnectionBundleSettingKey: bundle}
	cfg := appconfig.Config{CredentialKeyPath: keyPath, LLMAPIKey: "stale-environment-secret"}
	if err := loadReasoningConnection(context.Background(), settings, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.LLMAPIKey != "stored-provider-secret" || cfg.LLMModel != "fixture-model" || cfg.LLMBaseURL != profile.BaseURL {
		t.Fatal("runner did not use the saved reasoning connection")
	}
	missing := filepath.Join(t.TempDir(), "missing.key")
	cfg.CredentialKeyPath = missing
	if err := loadReasoningConnection(context.Background(), settings, &cfg); err == nil {
		t.Fatal("runner accepted a bundle without its original encryption key")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("runner created a replacement key after failing to decrypt saved credentials")
	}
}
