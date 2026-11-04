package agent

import (
	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/coderepair/sandbox"
	"github.com/Cyaside/Triovexa/internal/domain"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const handlerFixturePromptVersion = "handler-authority-fixture-v1"

func runInvestigationGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}

func investigationFixture(t *testing.T) (*sandbox.Workspace, coderepair.RepositoryBinding, coderepair.EvidenceSnapshot, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git executable unavailable")
	}
	root := t.TempDir()
	runInvestigationGit(t, root, "init", "-b", "main")
	runInvestigationGit(t, root, "config", "core.autocrlf", "false")
	path := filepath.Join(root, "internal", "workload", "repair_fixture.go")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package workload\n\nfunc accepts(version int) bool { return version != 1 }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal", "workload", "repair_regression_test.go"),
		[]byte("package workload\n// regression test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runInvestigationGit(t, root, "add", "internal/workload")
	runInvestigationGit(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid",
		"-c", "commit.gpgsign=false", "commit", "-m", "fixture")
	binding := coderepair.RepositoryBinding{ID: "binding-1", ServiceName: "queue-worker", Environment: "staging",
		RepositoryURL: "https://github.com/Cyaside/Triovexa", BaseRef: "main",
		AllowedPaths: []string{"internal/workload"}, TestRecipes: []string{"go-test-workload"},
		PolicyVersion: "repair-v1", Enabled: true}
	workspace, err := sandbox.Open(root, binding, sandbox.Limits{
		MaxFileBytes: 32 * 1024, MaxTotalBytes: 200 * 1024, MaxListedFiles: 20, MaxSearchHits: 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })
	now := time.Now().UTC()
	incident := domain.Incident{ID: "incident-1", ServiceName: binding.ServiceName, Environment: binding.Environment}
	snapshot, err := coderepair.BuildEvidenceSnapshot(incident, []domain.EvidenceItem{
		{ID: "log-1", IncidentID: incident.ID, Type: "log", Source: "grafana-loki", Timestamp: now,
			Snippet: "valid schema 2 jobs repeatedly fail with unsupported schema"},
		{ID: "metric-1", IncidentID: incident.ID, Type: "metric", Source: "workload-control", Timestamp: now,
			Snippet: "backlog rising", MetadataJSON: `{"target":"queue-worker","complete":true,"deployed_revision":"` + strings.Repeat("a", 40) + `"}`},
	}, now, coderepair.EvidenceLimits{MaxItems: 4, MaxSnippetBytes: 1024, MaxTotalBytes: 2048, MaxAge: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	return workspace, binding, snapshot, root
}
