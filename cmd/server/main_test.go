package main

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	appconfig "github.com/Cyaside/Triovexa/internal/config"
	"github.com/Cyaside/Triovexa/internal/secretstore"
	"github.com/Cyaside/Triovexa/internal/storage"
)

func TestApplyStoredConnectionProfilesRestoresEncryptedCredential(t *testing.T) {
	repository := storage.NewMemoryStore()
	cipher, err := secretstore.NewCipher(filepath.Join(t.TempDir(), "credential.key"), "")
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := cipher.Encrypt("persisted-secret")
	if err != nil {
		t.Fatal(err)
	}
	bundle := appconfig.ReasoningConnectionBundle{
		Profile: appconfig.ReasoningConnectionProfile{
			Provider: "openai-compatible", BaseURL: "https://api.example.com/v1", Model: "example-model",
			CredentialRef: "ENCRYPTED_LLM_API_KEY", JSONMode: true,
		},
		EncryptedAPIKey: encrypted, EncryptionVersion: 1,
	}
	raw, err := appconfig.EncodeConnectionProfile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.PutSetting(context.Background(), appconfig.ReasoningConnectionBundleSettingKey, raw); err != nil {
		t.Fatal(err)
	}
	cfg := appconfig.Config{}
	applyStoredConnectionProfiles(context.Background(), repository, &cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), cipher)
	provider, baseURL, apiKey, model := cfg.EffectiveLLM()
	if provider != "openai-compatible" || baseURL != "https://api.example.com/v1" || model != "example-model" || apiKey != "persisted-secret" {
		t.Fatalf("restored provider = %q %q %q key_configured=%t", provider, baseURL, model, apiKey != "")
	}
}
