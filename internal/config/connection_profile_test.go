package config

import (
	"os"
	"strings"
	"testing"
)

func TestConnectionProfilePersistsCredentialReferenceWithoutSecret(t *testing.T) {
	t.Setenv("TEST_PROVIDER_TOKEN", "super-secret-value")
	profile := ReasoningConnectionProfile{
		Provider: "openai-compatible", BaseURL: "http://127.0.0.1:11434/v1", Model: "local-model",
		CredentialRef: "TEST_PROVIDER_TOKEN", JSONMode: true,
	}
	raw, err := EncodeConnectionProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, os.Getenv("TEST_PROVIDER_TOKEN")) {
		t.Fatal("persisted connection profile contains the credential value")
	}
	decoded, err := DecodeReasoningConnectionProfile(raw)
	if err != nil {
		t.Fatal(err)
	}
	credential, ok := ResolveCredential(decoded.CredentialRef)
	if !ok || credential != "super-secret-value" {
		t.Fatalf("credential resolution failed: configured=%t value=%q", ok, credential)
	}
}

func TestConnectionProfileRejectsEmbeddedCredentials(t *testing.T) {
	profile := ReasoningConnectionProfile{Provider: "openai-compatible", BaseURL: "https://user:secret@example.com/v1", Model: "model", CredentialRef: "LLM_API_KEY"}
	if err := ValidateReasoningConnectionProfile(profile); err == nil {
		t.Fatal("expected embedded URL credentials to be rejected")
	}
}
