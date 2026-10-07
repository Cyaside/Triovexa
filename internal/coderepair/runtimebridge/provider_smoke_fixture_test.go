package runtimebridge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/coderepair/sandbox"
)

const compatibilityRegression = `//go:build repair_regression

package workload

import "testing"

func TestRepairFixtureAcceptsSchemaTwo(t *testing.T) {
	if rejects(2) { t.Fatal("repair fixture: unsupported job schema version 2") }
}
func TestSupportedSchemaCompatibility(t *testing.T) {
	for _, version := range []int{1, 2} {
		if rejects(version) { t.Errorf("supported schema %d rejected", version) }
	}
	for _, version := range []int{-1, 0, 3, 4, 99} {
		if !rejects(version) { t.Errorf("unsupported schema %d accepted", version) }
	}
}
`

// The live model receives a real baseline and protected regression suite, but
// never an expected patch. Tests protect existing behavior as well as the bug.
func compatibilityFixture(t *testing.T) (*sandbox.Workspace, coderepair.RepositoryBinding, string) {
	t.Helper()
	return compatibilityFixtureAt(t, t.TempDir())
}

func compatibilityFixtureAt(t *testing.T, root string) (*sandbox.Workspace, coderepair.RepositoryBinding, string) {
	t.Helper()
	// Only synthetic source is readable by the sandbox; no credentials live here.
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, root, "init", "-b", "main")
	fixtureGit(t, root, "config", "core.autocrlf", "false")
	path := filepath.Join(root, "internal", "workload", "worker.go")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"go.mod":                      "module fixture\n\ngo 1.24.0\n",
		"internal/workload/worker.go": "package workload\n\n// Schemas 1 and 2 are supported; other versions must be rejected.\nfunc rejects(version int) bool { return version != 1 }\n",
		"internal/workload/worker_regression_test.go": compatibilityRegression,
	} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	fixtureGit(t, root, "add", "go.mod", "internal/workload")
	fixtureGit(t, root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "schema compatibility fixture")
	base := fixtureGit(t, root, "rev-parse", "HEAD")
	binding := coderepair.RepositoryBinding{ID: "binding", ServiceName: "queue-worker", Environment: "staging",
		RepositoryURL: "https://github.com/Cyaside/Triovexa", BaseRef: "main", AllowedPaths: []string{"internal/workload"},
		TestRecipes: []string{"go-test-workload"}, PolicyVersion: "repair-v1", Enabled: true}
	w, err := sandbox.Open(root, binding, sandbox.Limits{MaxFileBytes: 32768, MaxTotalBytes: 204800, MaxListedFiles: 20, MaxSearchHits: 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w, binding, base
}

func TestProviderSmokeFixturePreservesCheckoutAfterClosure(t *testing.T) {
	root := filepath.Join(t.TempDir(), "retained-repository")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	var binding coderepair.RepositoryBinding
	var base string
	t.Run("closed workspace", func(t *testing.T) {
		_, binding, base = compatibilityFixtureAt(t, root)
	})
	if fixtureGit(t, root, "rev-parse", "HEAD") != base {
		t.Fatal("retained approval base disappeared after fixture cleanup")
	}
	w, err := sandbox.Open(root, binding, sandbox.Limits{MaxFileBytes: 32768, MaxTotalBytes: 204800, MaxListedFiles: 20, MaxSearchHits: 20})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if source, err := w.ReadFile("internal/workload/worker.go"); err != nil || !strings.Contains(string(source), "version != 1") {
		t.Fatal("retained approved source could not be reopened")
	}
}

func TestProviderSmokeFixtureRejectsSchemaCompatibilityRegressionIntegration(t *testing.T) {
	image := os.Getenv("TEST_REPAIR_SANDBOX_IMAGE")
	if image == "" {
		t.Skip("TEST_REPAIR_SANDBOX_IMAGE is required for isolated regression checks")
	}
	w, binding, _ := compatibilityFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	tester := sandbox.DockerTester{Image: image}
	path := filepath.Join(w.RootPath(), "internal", "workload", "worker.go")
	for _, scenario := range []struct {
		name, expression, proof string
		exit                    int
	}{
		{"baseline", "version != 1", "unsupported job schema version 2", 1},
		{"schema-one regression", "version != 2", "supported schema 1 rejected", 1},
		{"unrestricted versions", "false", "unsupported schema 3 accepted", 1},
		{"compatible fix", "version != 1 && version != 2", "ok", 0},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte("package workload\n\nfunc rejects(version int) bool { return "+scenario.expression+" }\n"), 0600); err != nil {
				t.Fatal(err)
			}
			result, err := tester.Run(ctx, w.RootPath(), binding, "go-test-workload")
			if err != nil || result.ExitCode != scenario.exit || result.TimedOut || result.Truncated || !strings.Contains(result.Output, scenario.proof) {
				t.Fatalf("compatibility check: exit=%d output=%s err=%v", result.ExitCode, result.Output, err)
			}
		})
	}
}
