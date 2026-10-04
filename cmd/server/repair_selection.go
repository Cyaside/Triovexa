package main

import (
	"os"
	"strings"

	"github.com/Cyaside/Triovexa/internal/ai"
	"github.com/Cyaside/Triovexa/internal/ai/modelgateway"
	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/coderepair/runtimebridge"
	"github.com/Cyaside/Triovexa/internal/secretstore"
)

func repairSelection(client *ai.OpenAICompatibleClient, cipher *secretstore.Cipher) func() (coderepair.AgentSelection, error) {
	return func() (coderepair.AgentSelection, error) {
		budget, err := modelgateway.LoadBudgetConfig(strings.TrimSpace(os.Getenv("AI_BUDGET_CONFIG_PATH")))
		if err != nil {
			return coderepair.AgentSelection{}, err
		}
		return runtimebridge.SealSelection(client, budget, cipher)
	}
}
