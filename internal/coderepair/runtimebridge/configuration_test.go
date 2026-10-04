package runtimebridge

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Cyaside/Triovexa/internal/ai"
	"github.com/Cyaside/Triovexa/internal/ai/admission"
	"github.com/Cyaside/Triovexa/internal/ai/modelgateway"
	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/secretstore"
)

func pinnedFixture(t *testing.T) (coderepair.Attempt, *ai.OpenAICompatibleClient, *secretstore.Cipher) {
	t.Helper()
	cipher, err := secretstore.NewCipher("", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	client, err := ai.NewOpenAICompatibleClient(ai.ProviderConfig{Name: "openai-compatible", Model: "fixture-model", BaseURL: "http://127.0.0.1:1/v1", APIKey: "private-fixture-value", AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	budget := modelgateway.BudgetConfig{ConfigVersion: "fixture-v1", Campaign: admission.Campaign{ID: "offline-campaign", Profile: "offline-fixture", Offline: true, MaxSpendMicroUSD: 200000, MaxRequests: 6, MaxInputTokens: 200000},
		Pricing: admission.Pricing{Version: "synthetic-v1", Provider: "openai-compatible", Model: "fixture-model"}, MaxInputTokens: 64000, MaxOutputTokens: 1500}
	selection, err := SealSelection(client, budget, cipher)
	if err != nil {
		t.Fatal(err)
	}
	attempt := coderepair.Attempt{ID: "attempt", CaseID: "case", Provider: selection.Provider, Model: selection.Model, PromptVersion: selection.PromptVersion, Runtime: selection.Runtime}
	attempt.Runtime.ThreadID = "case:attempt"
	return attempt, client, cipher
}

func TestSavedProviderRevisionIsPinnedPerAttempt(t *testing.T) {
	attempt, client, cipher := pinnedFixture(t)
	if err := client.Reconfigure(ai.ProviderConfig{Name: "openai-compatible", Model: "changed", BaseURL: "http://127.0.0.1:2/v1", APIKey: "changed-key", AllowHTTP: true}); err != nil {
		t.Fatal(err)
	}
	config, err := OpenConfiguration(attempt, cipher)
	if err != nil {
		t.Fatal(err)
	}
	if config.Provider.Model != "fixture-model" || config.Provider.BaseURL != "http://127.0.0.1:1/v1" || config.Provider.APIKey != "private-fixture-value" {
		t.Fatal("later UI configuration rewrote approved snapshot")
	}
	public, err := json.Marshal(attempt)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(public), "private-fixture") || strings.Contains(string(public), "sealed_snapshot") || strings.Contains(string(public), "127.0.0.1") {
		t.Fatal("attempt API exposes its private provider snapshot")
	}
}

func TestQueuedAttemptEngineMismatchBlocks(t *testing.T) {
	attempt, _, cipher := pinnedFixture(t)
	for name, mutate := range map[string]func(*coderepair.Attempt){
		"legacy": func(a *coderepair.Attempt) { a.Runtime = nil },
		"engine": func(a *coderepair.Attempt) { a.Runtime.EngineVersion = "old-version" },
		"thread": func(a *coderepair.Attempt) { a.Runtime.ThreadID = "other:attempt" },
		"digest": func(a *coderepair.Attempt) { a.Runtime.SnapshotSHA256 = strings.Repeat("0", 64) },
		"model":  func(a *coderepair.Attempt) { a.Model = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			copy := attempt
			spec := *attempt.Runtime
			copy.Runtime = &spec
			mutate(&copy)
			if _, err := OpenConfiguration(copy, cipher); err == nil {
				t.Fatal("incompatible attempt was silently migrated")
			}
		})
	}
}
