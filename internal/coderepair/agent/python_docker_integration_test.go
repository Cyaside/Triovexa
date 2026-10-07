package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/coderepair/sandbox"
	"github.com/Cyaside/Triovexa/internal/domain"
)

// No paid model or Go source is involved. The shared authority validates a real
// Python regression with the exact runtime and checks pinned by the binding.
func TestPythonInvestigationDockerRedGreenIntegration(t *testing.T) {
	image := os.Getenv("TEST_REPAIR_PYTHON_IMAGE")
	if image == "" {
		t.Skip("TEST_REPAIR_PYTHON_IMAGE is not configured")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"pyproject.toml":       "[project]\nname = 'repair-fixture'\nversion = '0.1.0'\n",
		"parser.py":            "def accepts(value):\n    return value > 1\n",
		"tests/test_parser.py": "import unittest\nfrom parser import accepts\n\nclass ParserTest(unittest.TestCase):\n    def test_accepts_one(self):\n        self.assertTrue(accepts(1), 'one rejected')\n    def test_rejects_zero(self):\n        self.assertFalse(accepts(0))\n",
	} {
		name = filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	runLoopGit(t, root, "init", "-b", "main")
	runLoopGit(t, root, "config", "core.autocrlf", "false")
	runLoopGit(t, root, "add", ".")
	runLoopGit(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "fixture")
	if err := os.WriteFile(filepath.Join(root, "parser.py"), []byte("def accepts(value):\n    return value > 0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := runLoopGit(t, root, "diff", "--", "parser.py")
	runLoopGit(t, root, "checkout", "--", "parser.py")
	b := coderepair.RepositoryBinding{ID: "python", ServiceName: "python-worker", Environment: "staging",
		RepositoryURL: "https://github.com/owner/python-worker", BaseRef: "main", Enabled: true,
		AllowedPaths: []string{"parser.py", "tests", "pyproject.toml"}, TestRecipes: []string{"python-tests"}, PolicyVersion: "repair-v2",
		ValidationProfile: &coderepair.ValidationProfile{ID: "python-tests", Version: "python-v1", Image: image,
			RootFiles: []string{"pyproject.toml"}, ProtectedPaths: []string{"tests"},
			Checks:           []coderepair.ValidationCommand{{Executable: "/usr/local/bin/python", Arguments: []string{"-m", "py_compile", "parser.py"}, TimeoutSeconds: 30}},
			Test:             coderepair.ValidationCommand{Executable: "/usr/local/bin/python", Arguments: []string{"-m", "unittest", "discover", "-s", "tests", "-v"}, TimeoutSeconds: 30},
			ExpectedTestName: "test_accepts_one", ExpectedFailure: "one rejected"}}
	w, err := sandbox.Open(root, b, sandbox.Limits{MaxFileBytes: 32 * 1024, MaxTotalBytes: 200 * 1024, MaxListedFiles: 20, MaxSearchHits: 20})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	now := time.Now().UTC()
	snapshot, err := coderepair.BuildEvidenceSnapshot(domain.Incident{ID: "incident", ServiceName: b.ServiceName, Environment: b.Environment}, []domain.EvidenceItem{
		{ID: "log-1", IncidentID: "incident", Type: "log", Source: "grafana-loki", Timestamp: now, Snippet: "parser rejects supported value 1"},
		{ID: "metric-1", IncidentID: "incident", Type: "metric", Source: "workload-control", Timestamp: now, Snippet: "backlog rising", MetadataJSON: `{"target":"python-worker","complete":true,"deployed_revision":"` + strings.Repeat("a", 40) + `"}`},
	}, now, coderepair.EvidenceLimits{MaxItems: 4, MaxSnippetBytes: 1024, MaxTotalBytes: 2048, MaxAge: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	model := &scriptedCompleter{decisions: []string{
		`{"operation":"read_file","path":"parser.py"}`,
		`{"operation":"propose_patch","hypothesis":"The boundary rejects valid value one","evidence_ids":["log-1","metric-1"],"patch":` + jsonString(t, patch) + `}`,
	}}
	loop, err := NewLoop(model, sandbox.DockerTester{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	result := loop.Investigate(ctx, w, b, snapshot, coderepair.AgentSelection{Provider: "openai-compatible", Model: "fixture-model", PromptVersion: PromptVersion}, "python-tests")
	if result.Status != coderepair.StatePatchReady || result.Before.ExitCode != 1 || result.After.ExitCode != 0 || result.PatchReport.SHA256 == "" {
		t.Fatalf("Python proof: status=%s code=%s before=%q after=%q", result.Status, result.Code, result.Before.Output, result.After.Output)
	}
}
