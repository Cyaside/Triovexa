package runtimebridge

import (
	"context"
	"errors"

	"github.com/Cyaside/Triovexa/internal/ai/admission"
	"github.com/Cyaside/Triovexa/internal/ai/modelgateway"
	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/coderepair/agent"
	"github.com/Cyaside/Triovexa/internal/coderepair/sandbox"
	"github.com/Cyaside/Triovexa/internal/secretstore"
)

// NativeRunner opens the configuration approved for this attempt. Legacy or
// incompatible queued attempts are blocked; it never silently chooses an engine.
type NativeRunner struct {
	Process Process
	Tests   agent.TestRunner
	Store   interface {
		ClaimStore
		coderepair.ToolReceiptStore
	}
	Ledger           *admission.Service
	Cipher           *secretstore.Cipher
	CheckpointDSN    string
	CheckpointSchema string
	// MaxModelRequests can reduce the trusted profile allowance for this run.
	// Zero keeps the profile default; it cannot grant a larger allowance.
	MaxModelRequests int
}

func nativeModelRequestLimit(profile string, ceiling int) (int, error) {
	limit := DefaultLimits().MaxModelRequests
	switch profile {
	case "offline-fixture", "final-smoke":
	case "internal":
		limit = 20
	default:
		return 0, errors.New("investigation execution profile is unavailable")
	}
	if ceiling < 0 || ceiling > limit {
		return 0, errors.New("investigation request limit exceeds the trusted profile allowance")
	}
	if ceiling > 0 {
		limit = ceiling
	}
	return limit, nil
}

func (n *NativeRunner) Investigate(ctx context.Context, w *sandbox.Workspace, b coderepair.RepositoryBinding, s coderepair.EvidenceSnapshot, selection coderepair.AgentSelection, recipe string) agent.InvestigationResult {
	fail := func(code string) agent.InvestigationResult {
		return agent.InvestigationResult{Status: coderepair.StateBlocked, Code: code, Reason: "The approved runtime configuration is unavailable or incompatible.", Provider: selection.Provider, Model: selection.Model, Prompt: selection.PromptVersion}
	}
	claim, ok := agent.InvestigationClaim(ctx)
	if !ok || n.Ledger == nil || n.Store == nil || n.Cipher == nil {
		return fail("INVALID_SCOPE")
	}
	config, err := OpenConfiguration(claim.Attempt, n.Cipher)
	if err != nil {
		return fail("ENGINE_VERSION_UNAVAILABLE")
	}
	ceiling := n.MaxModelRequests
	if claim.Attempt.Runtime.MaxModelRequests > 0 && (ceiling == 0 || claim.Attempt.Runtime.MaxModelRequests < ceiling) {
		ceiling = claim.Attempt.Runtime.MaxModelRequests
	}
	requestLimit, err := nativeModelRequestLimit(config.Budget.Campaign.Profile, ceiling)
	if err != nil {
		return fail("INVALID_SCOPE")
	}
	if err = n.Ledger.CreateCampaign(ctx, config.Budget.Campaign); err != nil {
		return fail("MODEL_DISPATCH_BLOCKED")
	}
	limits := DefaultLimits()
	limits.MaxModelRequests = requestLimit
	limits.MaxOutputTokens = config.Budget.MaxOutputTokens
	if config.Budget.RepairCandidateLimit > 0 {
		limits.MaxCandidateCount = config.Budget.RepairCandidateLimit
	}
	// The Go gateway accounts the full final payload; this independent byte
	// limit includes system/tool definitions rather than only user messages.
	limits.MaxInputBytes = int(config.Budget.MaxInputTokens) - 512
	inputBudgetMode := ""
	if config.Budget.Pricing.InputContract == modelgateway.GLMFlashInputContract {
		// Wire bytes and rendered-template tokens are different units. The named
		// contract is enforced by Go before reserving cost or opening the network;
		// JSON escaping must not impose the older raw-bytes token proxy here.
		limits.MaxInputBytes = MaxFrameBytes
		inputBudgetMode = "gateway-preview"
	}
	if limits.MaxInputBytes < 512 {
		return fail("CONTEXT_LIMIT")
	}
	engine := &Engine{Process: n.Process, Tests: n.Tests, Store: n.Store, Profile: config.Budget.Campaign.Profile,
		ConfigVersion: config.Budget.ConfigVersion, Limits: limits, Receipts: n.Store, Cipher: n.Cipher}
	engine.Accounting = func(c context.Context) (modelgateway.Summary, error) {
		return modelgateway.ReadSummary(c, n.Ledger, config.Budget.Campaign.ID, claim.Attempt.ID, "repair", limits.MaxModelRequests)
	}
	engine.Gateway = func(c context.Context, claimed agent.ClaimedInvestigation, fence Fence) (Transport, func(), error) {
		if n.CheckpointDSN == "" || n.CheckpointSchema == "" {
			return Transport{}, nil, errors.New("native investigation requires a checkpoint database")
		}
		gateway, err := modelgateway.New(modelgateway.Config{Provider: config.Provider, Pricing: config.Budget.Pricing, CampaignID: config.Budget.Campaign.ID,
			CaseID: claimed.Case.ID, AttemptID: claimed.Attempt.ID, Phase: "repair", ConfigVersion: config.Budget.ConfigVersion,
			MaxInputTokens: config.Budget.MaxInputTokens, MaxOutputTokens: config.Budget.MaxOutputTokens, MaxRequests: limits.MaxModelRequests,
			AllowedTools: modelgateway.WriterToolsForProfile(config.Budget.Campaign.Profile), Cipher: n.Cipher, Fence: fence}, n.Ledger)
		if err != nil {
			return Transport{}, nil, err
		}
		url, stop, err := gateway.Listen(c)
		return Transport{Model: config.Provider.Model, ModelGatewayURL: url, Capability: gateway.Capability(), CheckpointDSN: n.CheckpointDSN, CheckpointSchema: n.CheckpointSchema, InputBudgetMode: inputBudgetMode}, stop, err
	}
	return engine.Investigate(ctx, w, b, s, selection, recipe)
}
