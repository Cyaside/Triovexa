package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	repairagent "github.com/Cyaside/Triovexa/internal/coderepair/agent"
	"github.com/Cyaside/Triovexa/internal/coderepair/sandbox"
)

// The deterministic engine supplies a candidate, not an LLM protocol. The Go
// parser, patch/scope validation, formatter, tests and durable handler remain real.
type integrationRepairEngine struct {
	patch      string
	evidenceID string
	tests      *integrationRepairTester
	calls      int
}

func (e *integrationRepairEngine) Investigate(ctx context.Context, workspace *sandbox.Workspace,
	binding coderepair.RepositoryBinding, snapshot coderepair.EvidenceSnapshot,
	selection coderepair.AgentSelection, recipeID string) repairagent.InvestigationResult {
	e.calls++
	result := repairagent.InvestigationResult{Status: coderepair.StateBlocked,
		Provider: selection.Provider, Model: selection.Model, Prompt: selection.PromptVersion}
	claim, ok := repairagent.InvestigationClaim(ctx)
	if !ok || claim.RecoveryOnly || !snapshot.VerifyDigest() {
		result.Code = "INVALID_SCOPE"
		return result
	}
	recipe, err := sandbox.ResolveTestRecipe(binding, recipeID)
	if err != nil {
		result.Code = "TEST_UNAVAILABLE"
		return result
	}
	before, err := e.tests.Run(ctx, workspace.RootPath(), binding, recipeID)
	result.Before = before
	if err != nil || before.ExitCode == 0 || before.TimedOut || before.Truncated ||
		!strings.Contains(before.Output, recipe.ExpectedFailure) || !strings.Contains(before.Output, recipe.ExpectedTestName) {
		result.Code = "BASELINE_MISMATCH"
		return result
	}
	path := "internal/workload/repair_fixture.go"
	if _, err := workspace.ReadFile(path); err != nil {
		result.Code = "SOURCE_UNAVAILABLE"
		return result
	}
	encoded, _ := json.Marshal(repairagent.Decision{Operation: repairagent.ProposePatch,
		Patch: e.patch, Hypothesis: "Version check rejects supported schema 2", EvidenceIDs: []string{e.evidenceID}})
	decision, err := repairagent.ParseDecision(string(encoded), snapshot)
	if err != nil {
		result.Status, result.Code = coderepair.StateFailed, "INVALID_DECISION"
		return result
	}
	result = repairagent.VerifyCandidate(ctx, workspace, binding, e.tests, recipeID, decision,
		map[string]bool{path: true}, result)
	if result.Status != coderepair.StatePatchReady {
		result.Patch, result.PatchReport = "", sandbox.PatchReport{}
	}
	return result
}

type integrationRepairTester struct{ calls int }

func (s *integrationRepairTester) Run(_ context.Context, root string, _ coderepair.RepositoryBinding, recipeID string) (sandbox.TestResult, error) {
	s.calls++
	data, err := os.ReadFile(filepath.Join(root, "internal", "workload", "repair_fixture.go"))
	if err != nil {
		return sandbox.TestResult{}, err
	}
	if strings.Contains(string(data), "version != 1 && version != 2") {
		return sandbox.TestResult{RecipeID: recipeID, ExitCode: 0, Output: "ok"}, nil
	}
	return sandbox.TestResult{RecipeID: recipeID, ExitCode: 1,
		Output: "TestRepairFixtureAcceptsSchemaTwo: repair fixture: unsupported job schema version 2"}, nil
}

func testRepairGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	output, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v: %s", args, err, output)
	}
	return string(output)
}

func TestRepairHandlerRecordsVerifiedOutcomeIntegration(t *testing.T) {
	store := isolatedRepairStore(t)
	ctx := context.Background()
	root := t.TempDir()
	testRepairGit(t, root, "init", "-b", "main")
	testRepairGit(t, root, "config", "core.autocrlf", "false")
	file := filepath.Join(root, "internal", "workload", "repair_fixture.go")
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("package workload\n\nfunc accepts(version int) bool { return version != 1 }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	testRepairGit(t, root, "add", "internal/workload/repair_fixture.go")
	testRepairGit(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid",
		"-c", "commit.gpgsign=false", "commit", "-m", "fixture")
	if err := os.WriteFile(file, []byte("package workload\n\nfunc accepts(version int) bool { return version != 1 && version != 2 }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	patch := testRepairGit(t, root, "diff", "--", "internal/workload/repair_fixture.go")
	testRepairGit(t, root, "checkout", "--", "internal/workload/repair_fixture.go")
	base := strings.TrimSpace(testRepairGit(t, root, "rev-parse", "HEAD"))
	c, approval, attempt, job := makeInvestigationTestCase(t, store, 2)
	binding, err := store.GetRepositoryBinding(ctx, c.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	c.BaseSHA = base
	c.ScopeDigest, err = coderepair.ScopeDigest(c, binding)
	if err != nil {
		t.Fatal(err)
	}
	// Pin this private fixture commit before approval; the shared case helper
	// otherwise contains a synthetic SHA that is not present in this checkout.
	updated, err := store.db.ExecContext(ctx, `UPDATE repair_cases SET base_sha=$1,scope_digest=$2
		WHERE id=$3 AND state='awaiting_investigation_approval' AND version=$4`, base, c.ScopeDigest, c.ID, c.Version)
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := updated.RowsAffected(); err != nil || rows != 1 {
		t.Fatal("private fixture base was not pinned before approval")
	}
	approval.ScopeDigest = c.ScopeDigest
	attempt.Provider = "openai-compatible"
	attempt.Model = "fixture-model"
	attempt.PromptVersion = "handler-authority-fixture-v1"
	if ok, err := store.ApproveRepairInvestigation(ctx, approval, attempt, job,
		repairTestEvent(c.ID, "investigation_approved", time.Now().UTC())); err != nil || !ok {
		t.Fatalf("approve investigation: ok=%v err=%v", ok, err)
	}
	claimed, err := store.ClaimRepairJob(ctx, "worker-one", time.Now().UTC(), 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	tests := &integrationRepairTester{}
	engine := &integrationRepairEngine{patch: patch, evidenceID: evidenceLogID(t, store, c.ID), tests: tests}
	handler, err := repairagent.NewHandler(store, engine, func(_ context.Context, _ coderepair.RepositoryBinding, sha string) (string, func() error, error) {
		if sha != base {
			return "", nil, errors.New("fixture checkout does not match the approved revision")
		}
		return root, func() error { return nil }, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.Handle(ctx, claimed); err != nil {
		t.Fatal(err)
	}
	if engine.calls != 1 || tests.calls != 2 {
		t.Fatalf("handler did not use the real red-green authority once: engine=%d tests=%d", engine.calls, tests.calls)
	}
	if ok, err := store.CompleteRepairJob(ctx, claimed.ID, claimed.LeaseToken, time.Now().UTC()); err != nil || !ok {
		t.Fatalf("complete investigation: ok=%v err=%v", ok, err)
	}
	caseAfter, err := store.GetRepairCase(ctx, c.ID)
	if err != nil || caseAfter.State != coderepair.StatePatchReady || caseAfter.Version != c.Version+2 {
		t.Fatalf("case did not reach patch_ready: %+v %v", caseAfter, err)
	}
	attemptAfter, err := store.GetRepairAttempt(ctx, attempt.ID)
	if err != nil || attemptAfter.Status != coderepair.JobSucceeded {
		t.Fatalf("attempt did not commit with the verified outcome: %+v %v", attemptAfter, err)
	}
	storedPatch, err := store.GetRepairArtifactContent(ctx, attempt.ID, "patch")
	if err != nil || string(storedPatch) != patch {
		t.Fatalf("durable patch differs: %v %v", err, storedPatch)
	}
	report, err := store.GetRepairArtifactContent(ctx, attempt.ID, "investigation_report")
	if err != nil {
		t.Fatal(err)
	}
	var proof struct {
		Status coderepair.State `json:"status"`
		Before int              `json:"before_exit"`
		After  int              `json:"after_exit"`
		SHA    string           `json:"patch_sha256"`
	}
	if json.Unmarshal(report, &proof) != nil || proof.Status != coderepair.StatePatchReady ||
		proof.Before != 1 || proof.After != 0 || proof.SHA == "" {
		t.Fatal("durable report does not contain the Go-verified red-green proof")
	}
}

func evidenceLogID(t *testing.T, store *PostgresStore, caseID string) string {
	t.Helper()
	snapshot, err := store.GetRepairEvidenceSnapshot(context.Background(), caseID)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range snapshot.Entries {
		if entry.Type == "log" {
			return entry.ID
		}
	}
	t.Fatal("fixture snapshot has no log evidence")
	return ""
}
