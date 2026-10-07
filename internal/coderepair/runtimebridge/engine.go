package runtimebridge

import (
	"context"
	"errors"
	"time"

	"github.com/Cyaside/Triovexa/internal/ai/modelgateway"
	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/coderepair/agent"
	"github.com/Cyaside/Triovexa/internal/coderepair/sandbox"
	"github.com/Cyaside/Triovexa/internal/secretstore"
)

const EngineVersion = "1.14.1"
const PromptVersion = "repair-native-v5"
const PlaybookDigest = "df884ea9dec05f192518558f1928178910ba280a7e654a9a920a8549bd211c24"

type ClaimStore interface {
	GetRepairJob(context.Context, string) (coderepair.Job, error)
	GetRepairCase(context.Context, string) (coderepair.Case, error)
}

type GatewayFactory func(context.Context, agent.ClaimedInvestigation, Fence) (Transport, func(), error)

type Engine struct {
	Process       Process
	Tests         agent.TestRunner
	Store         ClaimStore
	Gateway       GatewayFactory
	Profile       string
	ConfigVersion string
	Limits        Limits
	Receipts      coderepair.ToolReceiptStore
	Cipher        *secretstore.Cipher
	Accounting    func(context.Context) (modelgateway.Summary, error)
}

var _ agent.InvestigationEngine = (*Engine)(nil)

func (e *Engine) Investigate(ctx context.Context, workspace *sandbox.Workspace, binding coderepair.RepositoryBinding,
	snapshot coderepair.EvidenceSnapshot, selection coderepair.AgentSelection, recipeID string) (result agent.InvestigationResult) {
	result = agent.InvestigationResult{Status: coderepair.StateBlocked, Provider: selection.Provider, Model: selection.Model, Prompt: selection.PromptVersion}
	defer func() {
		if e.Accounting != nil {
			accountingCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			summary, err := e.Accounting(accountingCtx)
			if err != nil {
				summary.UsageStatus = "unknown"
			}
			result.Usage = summary.Usage
			result.Runtime = &agent.RuntimeTrace{EngineID: "deepagents", EngineVersion: EngineVersion, ContractVersion: ContractVersion, ConfigVersion: e.ConfigVersion, Profile: e.Profile, Accounting: summary}
		}
	}()
	fail := func(code, reason string) agent.InvestigationResult {
		result.Code, result.Reason = code, reason
		return result
	}
	claim, ok := agent.InvestigationClaim(ctx)
	if !ok || e.Store == nil || e.Tests == nil || claim.Case.ID != claim.Job.CaseID || claim.Attempt.ID != claim.Job.AttemptID {
		return fail("INVALID_SCOPE", "native engine requires a claimed approved investigation")
	}
	if e.Process.Version != EngineVersion || selection.PromptVersion != PromptVersion {
		return fail("ENGINE_VERSION_UNAVAILABLE", "attempt was approved for a different engine or prompt version")
	}
	if e.Profile != "offline-fixture" && e.Profile != "final-smoke" && e.Profile != "internal" {
		return fail("INVALID_SCOPE", "investigation execution profile is unavailable")
	}
	if e.Profile != "internal" && e.Limits.MaxCandidateCount != 1 {
		return fail("INVALID_SCOPE", "this profile permits only one patch candidate")
	}
	fence := LeaseFence(e.Store, claim)
	tools, err := NewToolSession(workspace, binding, snapshot, selection, recipeID, e.Tests, fence, e.Limits, claim.Case.BaseSHA)
	if err != nil {
		return fail("INVALID_SCOPE", "approved native tool scope is invalid")
	}
	if e.Receipts != nil {
		if err := tools.RestoreReceipts(ctx, e.Receipts, claim, e.Cipher); err != nil {
			return fail("CHECKPOINT_INCOMPATIBLE", "durable tool receipts could not be safely reconciled")
		}
		if tools.terminal {
			// A fenced, validated Go receipt already contains the final proof.
			// Reopening transport or rerunning tests cannot strengthen it.
			return tools.Result()
		}
	}
	if claim.RecoveryOnly {
		return fail("EVIDENCE_STALE", "approved evidence is too old to dispatch a new investigation")
	}
	baseline, err := tools.Baseline(ctx)
	if err != nil {
		if errors.Is(err, errBaselineReceipt) {
			return fail("TOOL_DISPATCH_UNCERTAIN", "durable regression baseline could not be safely reconciled")
		}
		return fail("BASELINE_MISMATCH", "approved regression baseline did not reproduce the expected failure")
	}
	if e.Gateway == nil {
		return fail("MODEL_DISPATCH_BLOCKED", "provider admission transport is unavailable")
	}
	transport, stopGateway, err := e.Gateway(ctx, claim, fence)
	if err != nil {
		return fail("MODEL_DISPATCH_BLOCKED", "provider admission or checkpoint transport is unavailable")
	}
	if stopGateway == nil {
		return fail("INVALID_SCOPE", "gateway did not supply owned cleanup")
	}
	defer stopGateway()
	if transport.Model != selection.Model {
		return fail("PROVIDER_CHANGED", "attempt model does not match the pinned provider")
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return fail("DEADLINE_EXCEEDED", "investigation requires a bounded deadline")
	}
	if deadline.After(time.Now().Add(20 * time.Minute)) {
		return fail("DEADLINE_EXCEEDED", "investigation deadline exceeds 20 minutes")
	}
	start := Start{Scope: Scope{CaseID: claim.Job.CaseID, AttemptID: claim.Job.AttemptID, EngineID: "deepagents",
		EngineVersion: EngineVersion, ContractVersion: 1, CheckpointThread: claim.Job.CaseID + ":" + claim.Job.AttemptID,
		Provider: selection.Provider, ProviderConfigVersion: e.ConfigVersion, PromptVersion: PromptVersion,
		PlaybookManifestDigest: PlaybookDigest, BaseSHA: claim.Case.BaseSHA, DeployedSHA: snapshot.DeployedRevision,
		AllowedPaths: append([]string(nil), binding.AllowedPaths...), RecipeIDs: []string{recipeID}, Profile: e.Profile,
		Deadline: Deadline(deadline), Evidence: snapshot, Baseline: baseline, Limits: e.Limits}, Transport: transport}
	child, err := e.Process.Run(ctx, start, tools)
	result = tools.Result()
	if err != nil {
		if result.Status == coderepair.StatePatchReady {
			return result
		} // Go proof remains authoritative even if child exits after terminal acknowledgement.
		if errors.Is(ctx.Err(), context.Canceled) {
			return fail("CANCELLED", "investigation was cancelled")
		}
		return fail("RUNTIME_FAILED", err.Error())
	}
	if !tools.terminal {
		if child.Status == "failed" {
			result.Status = coderepair.StateFailed
		}
		return fail(child.Code, child.Reason)
	}
	return result
}
