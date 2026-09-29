package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/coderepair/sandbox"
	"github.com/Cyaside/Triovexa/internal/domain"
)

func TestInvestigationDockerRedGreenIntegration(t *testing.T) {
	image := os.Getenv("TEST_REPAIR_SANDBOX_IMAGE")
	if image == "" {
		t.Skip("set TEST_REPAIR_SANDBOX_IMAGE to run the Docker Desktop integration gate")
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Clean(filepath.Join(workingDirectory, "..", "..", ".."))
	checkout := filepath.Join(t.TempDir(), "checkout")
	clone := exec.Command("git", "clone", "--quiet", "--local", "--no-hardlinks", "--no-checkout", source, checkout)
	if output, err := clone.CombinedOutput(); err != nil {
		t.Fatalf("clone isolated fixture checkout: %v: %s", err, output)
	}
	runLoopGit(t, checkout, "config", "--local", "core.autocrlf", "false")
	runLoopGit(t, checkout, "checkout", "--quiet", "HEAD")
	file := filepath.Join(checkout, "internal", "workload", "repair_fixture.go")
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(before), "if version != 1 {", "if version != 1 && version != 2 {", 1)
	if updated == string(before) {
		t.Fatal("fixture source no longer contains the expected version check")
	}
	if err := os.WriteFile(file, []byte(updated), 0600); err != nil {
		t.Fatal(err)
	}
	patch := runLoopGit(t, checkout, "diff", "--", "internal/workload/repair_fixture.go")
	runLoopGit(t, checkout, "checkout", "--", "internal/workload/repair_fixture.go")
	binding := coderepair.RepositoryBinding{ID: "binding-1", ServiceName: "queue-worker", Environment: "staging",
		RepositoryURL: "https://github.com/Cyaside/Triovexa", BaseRef: "main", Enabled: true,
		AllowedPaths: []string{"internal/workload"}, TestRecipes: []string{"go-test-workload"}, PolicyVersion: "repair-v1"}
	workspace, err := sandbox.Open(checkout, binding, sandbox.Limits{
		MaxFileBytes: 32 * 1024, MaxTotalBytes: 200 * 1024, MaxListedFiles: 20, MaxSearchHits: 20})
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	if status := runLoopGit(t, checkout, "status", "--porcelain", "--untracked-files=all"); status != "" {
		t.Fatalf("fixture checkout is dirty before patch: %q", status)
	}
	validationCtx, validationCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if _, err := workspace.ValidatePatch(validationCtx, []byte(patch), sandbox.PatchLimits{
		MaxPatchBytes: 64 * 1024, MaxFiles: 5, MaxChangedLines: 300}); err != nil {
		validationCancel()
		t.Fatalf("fixture patch is invalid before investigation: %v", err)
	}
	validationCancel()
	now := time.Now().UTC()
	incident := domain.Incident{ID: "incident-1", ServiceName: binding.ServiceName, Environment: binding.Environment}
	snapshot, err := coderepair.BuildEvidenceSnapshot(incident, []domain.EvidenceItem{
		{ID: "log-1", IncidentID: incident.ID, Type: "log", Source: "grafana-loki", Timestamp: now,
			Snippet: "schema version 2 jobs fail despite worker restart"},
		{ID: "metric-1", IncidentID: incident.ID, Type: "metric", Source: "workload-control", Timestamp: now,
			Snippet: "queue backlog rises", MetadataJSON: `{"target":"queue-worker","complete":true,"deployed_revision":"` + strings.Repeat("a", 40) + `"}`},
	}, now, coderepair.EvidenceLimits{MaxItems: 4, MaxSnippetBytes: 1024, MaxTotalBytes: 2048, MaxAge: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	model := &scriptedCompleter{decisions: []string{
		`{"operation":"read_file","path":"internal/workload/repair_fixture.go"}`,
		`{"operation":"propose_patch","hypothesis":"The version check excludes valid schema 2 jobs","evidence_ids":["log-1","metric-1"],"patch":` + jsonString(t, patch) + `}`,
	}}
	loop, err := NewLoop(model, sandbox.DockerTester{Image: image})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	result := loop.Investigate(ctx, workspace, binding, snapshot, coderepair.AgentSelection{
		Provider: "openai-compatible", Model: "fixture-model", PromptVersion: PromptVersion}, "go-test-workload")
	if result.Status != coderepair.StatePatchReady || result.Before.ExitCode != 1 || result.After.ExitCode != 0 ||
		result.PatchReport.SHA256 == "" {
		t.Fatalf("real container regression did not turn red to green: status=%s code=%s reason=%s before=%q after=%q",
			result.Status, result.Code, result.Reason, result.Before.Output, result.After.Output)
	}
}
