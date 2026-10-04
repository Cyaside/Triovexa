package runtimebridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/coderepair/agent"
	"github.com/Cyaside/Triovexa/internal/coderepair/sandbox"
)

func validateToolSnapshot(ctx context.Context, state toolSnapshot, session *ToolSession) error {
	invalid := errors.New("tool snapshot does not match its approved proof and limits")
	if session == nil || state.Result.Provider != session.result.Provider || state.Result.Model != session.result.Model || state.Result.Prompt != session.result.Prompt ||
		(!validReceiptToolName(state.Name) && state.Name != "baseline") || !validReceiptCallID(state.Response.CallID) || !validSnapshotDigest(state.ArgsSHA256) ||
		state.Result.Steps < 0 || state.Result.Steps > session.limits.MaxToolSteps || state.Candidates < 0 || state.Candidates > session.limits.MaxCandidateCount ||
		state.CandidateDigests == nil || len(state.CandidateDigests) > state.Candidates || len(state.ReadPaths) > session.limits.MaxToolSteps || len(state.Repeated) > session.limits.MaxToolSteps ||
		(state.Response.Status != "ok" && state.Response.Status != "error") {
		return invalid
	}
	for path, read := range state.ReadPaths {
		if !read || !session.binding.AllowsPath(path) {
			return invalid
		}
	}
	for digest, count := range state.Repeated {
		if !validSnapshotDigest(digest) || count < 1 || count > 3 {
			return invalid
		}
	}
	for digest, proposed := range state.CandidateDigests {
		if !proposed || !validSnapshotDigest(digest) {
			return invalid
		}
	}
	recipe, err := sandbox.ResolveTestRecipe(session.binding, session.recipeID)
	before := state.Result.Before
	if err != nil || before.RecipeID != session.recipeID || before.ExitCode == 0 || before.TimedOut || before.Truncated || before.Duration < 0 || len(before.Output) > recipe.MaxOutputBytes ||
		!strings.Contains(before.Output, recipe.ExpectedFailure) || !strings.Contains(before.Output, recipe.ExpectedTestName) {
		return invalid
	}
	if state.Result.Status != coderepair.StatePatchReady {
		if state.Result.Status != coderepair.StateBlocked && state.Result.Status != coderepair.StateFailed {
			return invalid
		}
		if state.Result.Patch != "" || state.Result.PatchReport.SHA256 != "" {
			return invalid
		}
		if !state.Terminal && state.Result.Code != "" {
			if state.Candidates < 1 || state.Candidates >= session.limits.MaxCandidateCount {
				return invalid
			}
			switch state.Result.Code {
			case "INVALID_PATCH", "PATCH_APPLY_FAILED", "FORMAT_FAILED", "TESTS_FAILED":
			default:
				return invalid
			}
		}
		return nil
	}
	return validateRestoredPatch(ctx, state, session, recipe)
}

func validateRestoredPatch(ctx context.Context, state toolSnapshot, session *ToolSession, recipe sandbox.TestRecipe) error {
	invalid := errors.New("restored patch has no consistent red-to-green proof")
	result := state.Result
	after := result.After
	if !state.Terminal || state.Candidates < 1 || result.Code != "PATCH_VERIFIED" || after.RecipeID != session.recipeID || after.ExitCode != 0 || after.TimedOut || after.Truncated || after.Duration < 0 || len(after.Output) > recipe.MaxOutputBytes ||
		result.Patch == "" || len(result.Patch) > 64*1024 {
		return invalid
	}
	digest := sha256.Sum256([]byte(result.Patch))
	if hex.EncodeToString(digest[:]) != result.PatchReport.SHA256 {
		return invalid
	}
	normalized := sha256.Sum256([]byte(strings.TrimSpace(result.Patch)))
	if !state.CandidateDigests[hex.EncodeToString(normalized[:])] {
		return invalid
	}
	decision, _ := json.Marshal(agent.Decision{Operation: agent.ProposePatch, Patch: result.Patch, Hypothesis: result.Hypothesis, EvidenceIDs: result.EvidenceIDs})
	if _, err := agent.ParseDecision(string(decision), session.snapshot); err != nil {
		return invalid
	}
	for _, path := range result.PatchReport.Files {
		if !state.ReadPaths[path] || !session.binding.AllowsPath(path) || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return invalid
		}
	}
	// Reuse the same Go patch authority as initial dispatch, without applying a
	// patch or rerunning a recorded test. The fresh checkout remains at base.
	patchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	report, err := session.workspace.ValidatePatch(patchCtx, []byte(result.Patch), sandbox.PatchLimits{MaxPatchBytes: 64 * 1024, MaxFiles: 5, MaxChangedLines: 300})
	if err != nil || report.SHA256 != result.PatchReport.SHA256 || !slices.Equal(report.Files, result.PatchReport.Files) || report.AddedLines != result.PatchReport.AddedLines || report.RemovedLines != result.PatchReport.RemovedLines {
		return invalid
	}
	return nil
}

func validSnapshotDigest(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validReceiptToolName(name string) bool {
	switch name {
	case "repo_list", "repo_read", "repo_search", "run_test_recipe", "propose_patch", "cannot_determine":
		return true
	default:
		return false
	}
}

func validReceiptCallID(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || strings.ContainsRune("_.:-", character) {
			continue
		}
		return false
	}
	return true
}
