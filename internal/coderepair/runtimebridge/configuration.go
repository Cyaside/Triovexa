package runtimebridge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/Cyaside/Triovexa/internal/ai"
	"github.com/Cyaside/Triovexa/internal/ai/modelgateway"
	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/secretstore"
)

const CheckpointVersion = "1"

type PinnedConfiguration struct {
	Provider ai.ProviderConfig         `json:"provider"`
	Budget   modelgateway.BudgetConfig `json:"budget"`
}

func SealSelection(client *ai.OpenAICompatibleClient, budget modelgateway.BudgetConfig, cipher *secretstore.Cipher) (coderepair.AgentSelection, error) {
	if client == nil || !client.Configured() || cipher == nil || budget.Validate() != nil {
		return coderepair.AgentSelection{}, errors.New("investigation requires model admission configuration")
	}
	provider := client.ConfigurationSnapshot()
	if budget.Pricing.Provider != provider.Name || budget.Pricing.Model != provider.Model {
		return coderepair.AgentSelection{}, errors.New("investigation model does not match admission pricing")
	}
	plain, err := json.Marshal(PinnedConfiguration{Provider: provider, Budget: budget})
	if err != nil {
		return coderepair.AgentSelection{}, err
	}
	sealed, err := cipher.Encrypt(string(plain))
	if err != nil {
		return coderepair.AgentSelection{}, errors.New("investigation configuration could not be sealed")
	}
	digest := sha256.Sum256(plain)
	return coderepair.AgentSelection{Provider: provider.Name, Model: provider.Model, PromptVersion: PromptVersion,
		Runtime: &coderepair.RuntimeSpec{EngineID: "deepagents", EngineVersion: EngineVersion, ContractVersion: ContractVersion,
			CheckpointVersion: CheckpointVersion, Profile: budget.Campaign.Profile, ConfigVersion: budget.ConfigVersion,
			CampaignID: budget.Campaign.ID, PlaybookDigest: PlaybookDigest, SnapshotSHA256: hex.EncodeToString(digest[:]), SealedSnapshot: sealed}}, nil
}

func OpenConfiguration(attempt coderepair.Attempt, cipher *secretstore.Cipher) (PinnedConfiguration, error) {
	spec := attempt.Runtime
	if spec == nil || spec.Validate() != nil || spec.EngineID != "deepagents" || spec.EngineVersion != EngineVersion ||
		spec.ContractVersion != ContractVersion || spec.CheckpointVersion != CheckpointVersion || spec.PlaybookDigest != PlaybookDigest ||
		spec.ThreadID != attempt.CaseID+":"+attempt.ID || attempt.PromptVersion != PromptVersion || cipher == nil {
		return PinnedConfiguration{}, errors.New("ENGINE_VERSION_UNAVAILABLE")
	}
	plain, err := cipher.Decrypt(spec.SealedSnapshot)
	if err != nil {
		return PinnedConfiguration{}, errors.New("CHECKPOINT_INCOMPATIBLE")
	}
	digest := sha256.Sum256([]byte(plain))
	if hex.EncodeToString(digest[:]) != spec.SnapshotSHA256 {
		return PinnedConfiguration{}, errors.New("CHECKPOINT_INCOMPATIBLE")
	}
	var config PinnedConfiguration
	if DecodeStrict([]byte(plain), &config) != nil || config.Budget.Validate() != nil ||
		config.Budget.Campaign.ID != spec.CampaignID || config.Budget.Campaign.Profile != spec.Profile || config.Budget.ConfigVersion != spec.ConfigVersion ||
		config.Provider.Name != attempt.Provider || config.Provider.Model != attempt.Model ||
		config.Budget.Pricing.Model != attempt.Model || config.Budget.Pricing.Provider != attempt.Provider {
		return PinnedConfiguration{}, errors.New("CHECKPOINT_INCOMPATIBLE")
	}
	return config, nil
}
