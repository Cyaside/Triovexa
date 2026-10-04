package main

import (
	"context"
	"os"
	"strings"

	"github.com/Cyaside/Triovexa/internal/ai"
	"github.com/Cyaside/Triovexa/internal/ai/admission"
	"github.com/Cyaside/Triovexa/internal/ai/modelgateway"
	"github.com/Cyaside/Triovexa/internal/secretstore"
	"github.com/Cyaside/Triovexa/internal/storage"
)

// Missing pricing/ledger configuration blocks inference while keeping the
// console available to diagnose configuration. It never installs a permissive
// transport or an in-memory replacement for a durable paid campaign.
func configureModelAdmission(ctx context.Context, repository storage.Repository, cipher *secretstore.Cipher) ai.ModelDispatcher {
	path := strings.TrimSpace(os.Getenv("AI_BUDGET_CONFIG_PATH"))
	config, err := modelgateway.LoadBudgetConfig(path)
	if err != nil {
		return modelgateway.BlockedDispatcher{Reason: err}
	}
	provider, ok := repository.(interface{ ModelBudgetStore() admission.Store })
	if !ok {
		return modelgateway.BlockedDispatcher{Reason: admission.ErrInvalid}
	}
	ledger, err := admission.NewService(provider.ModelBudgetStore())
	if err != nil {
		return modelgateway.BlockedDispatcher{Reason: err}
	}
	dispatcher, err := modelgateway.NewSharedDispatcher(ctx, config, ledger, cipher)
	if err != nil {
		return modelgateway.BlockedDispatcher{Reason: err}
	}
	return dispatcher
}
