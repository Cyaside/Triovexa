package agent

import (
	"bytes"
	"context"
	"go/format"
	"strings"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/coderepair/sandbox"
)

// VerifyCandidate is the authority boundary shared during engine migration.
// A graph terminal message alone can never produce a publishable patch.
func VerifyCandidate(ctx context.Context, workspace *sandbox.Workspace, binding coderepair.RepositoryBinding,
	tests TestRunner, recipeID string, decision Decision, readPaths map[string]bool, result InvestigationResult) InvestigationResult {
	fail := func(state coderepair.State, code, reason string) InvestigationResult {
		result.Status, result.Code, result.Reason = state, code, reason
		return result
	}
	if len(readPaths) == 0 {
		return fail(coderepair.StateFailed, "UNGROUNDED_PATCH", "patch was proposed without reading source")
	}
	limits := sandbox.PatchLimits{MaxPatchBytes: 64 * 1024, MaxFiles: 5, MaxChangedLines: 300}
	patchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	report, err := workspace.ValidatePatch(patchCtx, []byte(decision.Patch), limits)
	cancel()
	if err != nil {
		return fail(coderepair.StateFailed, "INVALID_PATCH", "proposed patch did not pass scope validation")
	}
	// The Go tool owner may use this private report to restore a rejected
	// candidate. Callers must clear failed proof before persisting an outcome.
	result.PatchReport = report
	for _, path := range report.Files {
		if !readPaths[path] || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return fail(coderepair.StateFailed, "UNGROUNDED_PATCH", "patch changed an unread or protected test file")
		}
	}
	patchCtx, cancel = context.WithTimeout(ctx, 10*time.Second)
	report, err = workspace.ApplyPatch(patchCtx, []byte(decision.Patch), limits)
	cancel()
	if err != nil {
		return fail(coderepair.StateFailed, "PATCH_APPLY_FAILED", "validated patch could not be applied")
	}
	for _, path := range report.Files {
		content, readErr := workspace.ReadFile(path)
		formatted, formatErr := format.Source(content)
		if readErr != nil || formatErr != nil || !bytes.Equal(content, formatted) {
			return fail(coderepair.StateFailed, "FORMAT_FAILED", "patched Go source does not pass gofmt")
		}
	}
	after, err := tests.Run(ctx, workspace.RootPath(), binding, recipeID)
	result.After = after
	if err != nil || after.TimedOut || after.Truncated {
		return fail(coderepair.StateBlocked, "TEST_UNAVAILABLE", "isolated post-patch test could not complete")
	}
	if after.ExitCode != 0 {
		return fail(coderepair.StateFailed, "TESTS_FAILED", "regression test still fails after the patch")
	}
	result.Status, result.Code = coderepair.StatePatchReady, "PATCH_VERIFIED"
	result.Hypothesis, result.EvidenceIDs = decision.Hypothesis, append([]string(nil), decision.EvidenceIDs...)
	result.Patch, result.PatchReport = decision.Patch, report
	return result
}
