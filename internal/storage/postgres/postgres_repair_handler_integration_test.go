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

	"github.com/Cyaside/Triovexa/internal/ai"
	"github.com/Cyaside/Triovexa/internal/coderepair"
	repairagent "github.com/Cyaside/Triovexa/internal/coderepair/agent"
	"github.com/Cyaside/Triovexa/internal/coderepair/sandbox"
)

type integrationRepairModel struct {
	responses []string
	index     int
}

func (m *integrationRepairModel) CompleteJSONDetailed(_ context.Context, _ []ai.ChatMessage) (ai.CompletionResult, error) {
	if m.index >= len(m.responses) {
		return ai.CompletionResult{}, errors.New("model script exhausted")
	}
	response := m.responses[m.index]
	m.index++
	return ai.CompletionResult{Content: response, Provider: "openai-compatible", Model: "fixture-model"}, nil
}

type integrationRepairTester struct{}

func (integrationRepairTester) Run(_ context.Context, root string, _ coderepair.RepositoryBinding, recipeID string) (sandbox.TestResult, error) {
	data, err := os.ReadFile(filepath.Join(root, "internal", "workload", "repair_fixture.go"))
	if err != nil {
		return sandbox.TestResult{}, err
	}
	if strings.Contains(string(data), "version != 2") {
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
	c, approval, attempt, job := makeInvestigationTestCase(t, store, 2)
	attempt.Provider = "openai-compatible"
	attempt.Model = "fixture-model"
	attempt.PromptVersion = repairagent.PromptVersion
	if ok, err := store.ApproveRepairInvestigation(ctx, approval, attempt, job,
		repairTestEvent(c.ID, "investigation_approved", time.Now().UTC())); err != nil || !ok {
		t.Fatalf("approve investigation: ok=%v err=%v", ok, err)
	}
	claimed, err := store.ClaimRepairJob(ctx, "worker-one", time.Now().UTC(), 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
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
	if err := os.WriteFile(file, []byte("package workload\n\nfunc accepts(version int) bool { return version != 2 }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	patch := testRepairGit(t, root, "diff", "--", "internal/workload/repair_fixture.go")
	testRepairGit(t, root, "checkout", "--", "internal/workload/repair_fixture.go")
	encodedPatch, _ := json.Marshal(patch)
	model := &integrationRepairModel{responses: []string{
		`{"operation":"read_file","path":"internal/workload/repair_fixture.go"}`,
		`{"operation":"propose_patch","hypothesis":"Version check rejects valid schema 2","evidence_ids":["` + evidenceLogID(t, store, c.ID) + `"],"patch":` + string(encodedPatch) + `}`,
	}}
	loop, err := repairagent.NewLoop(model, integrationRepairTester{})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := repairagent.NewHandler(store, loop, func(context.Context, coderepair.RepositoryBinding, string) (string, func() error, error) {
		return root, func() error { return nil }, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.Handle(ctx, claimed); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.CompleteRepairJob(ctx, claimed.ID, claimed.LeaseToken, time.Now().UTC()); err != nil || !ok {
		t.Fatalf("complete investigation: ok=%v err=%v", ok, err)
	}
	caseAfter, err := store.GetRepairCase(ctx, c.ID)
	if err != nil || caseAfter.State != coderepair.StatePatchReady {
		t.Fatalf("case did not reach patch_ready: %+v %v", caseAfter, err)
	}
	storedPatch, err := store.GetRepairArtifactContent(ctx, attempt.ID, "patch")
	if err != nil || string(storedPatch) != patch {
		t.Fatalf("durable patch differs: %v %v", err, storedPatch)
	}
	if _, err := store.GetRepairArtifactContent(ctx, attempt.ID, "investigation_report"); err != nil {
		t.Fatal(err)
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
