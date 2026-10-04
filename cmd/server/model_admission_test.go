package main

import (
	"context"
	"errors"
	"testing"

	"github.com/Cyaside/Triovexa/internal/ai"
	"github.com/Cyaside/Triovexa/internal/ai/admission"
	"github.com/Cyaside/Triovexa/internal/storage/memory"
)

func TestMissingAdmissionConfigurationDoesNotInstallNetworkTransport(t *testing.T) {
	t.Setenv("AI_BUDGET_CONFIG_PATH", "")
	store := memory.NewMemoryStore()
	dispatcher := configureModelAdmission(context.Background(), store, nil)
	if _, _, _, err := dispatcher.Dispatch(context.Background(), ai.ProviderConfig{BaseURL: "https://never-send.example.invalid", APIKey: "dummy"}, []byte(`{}`)); !errors.Is(err, admission.ErrPricingUnknown) {
		t.Fatalf("missing budget config did not block inference: %v", err)
	}
}
