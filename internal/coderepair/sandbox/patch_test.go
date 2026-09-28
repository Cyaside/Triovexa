package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func patchWorkspace(t *testing.T) (*Workspace, string, context.Context) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git executable is unavailable")
	}
	directory := t.TempDir()
	runTestGit(t, directory, "init", "-b", "main")
	runTestGit(t, directory, "config", "core.autocrlf", "false")
	file := filepath.Join(directory, "internal", "workload", "worker.go")
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("package workload\nfunc old() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, directory, "add", "internal/workload/worker.go")
	runTestGit(t, directory, "-c", "user.name=Test", "-c", "user.email=test@example.invalid",
		"-c", "commit.gpgsign=false", "commit", "-m", "fixture")
	w, err := Open(directory, testBinding(), Limits{MaxFileBytes: 1024, MaxTotalBytes: 2048, MaxListedFiles: 10, MaxSearchHits: 10})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return w, directory, ctx
}

func defaultPatchLimits() PatchLimits {
	return PatchLimits{MaxPatchBytes: 64 * 1024, MaxFiles: 5, MaxChangedLines: 200}
}

func TestValidateAndApplyScopedPatch(t *testing.T) {
	w, directory, ctx := patchWorkspace(t)
	file := filepath.Join(directory, "internal", "workload", "worker.go")
	if err := os.WriteFile(file, []byte("package workload\nfunc fixed() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	patch := []byte(runTestGit(t, directory, "diff", "--", "internal/workload/worker.go") + "\n")
	runTestGit(t, directory, "checkout", "--", "internal/workload/worker.go")
	report, err := w.ValidatePatch(ctx, patch, defaultPatchLimits())
	if err != nil || len(report.Files) != 1 || report.Files[0] != "internal/workload/worker.go" ||
		report.AddedLines != 1 || report.RemovedLines != 1 || len(report.SHA256) != 64 {
		t.Fatalf("validate scoped patch: report=%+v err=%v", report, err)
	}
	applied, err := w.ApplyPatch(ctx, patch, defaultPatchLimits())
	if err != nil || applied.SHA256 != report.SHA256 {
		t.Fatalf("apply scoped patch: report=%+v err=%v", applied, err)
	}
	content, err := os.ReadFile(file)
	if err != nil || !strings.Contains(string(content), "fixed") {
		t.Fatalf("patch did not modify source: %q err=%v", content, err)
	}
	if _, err := w.ApplyPatch(ctx, patch, defaultPatchLimits()); err == nil {
		t.Fatal("applied patch to a dirty checkout")
	}
}

func TestPatchRejectsOutOfScopeAndStructuralChanges(t *testing.T) {
	w, directory, ctx := patchWorkspace(t)
	outside := filepath.Join(directory, "README.md")
	if err := os.WriteFile(outside, []byte("old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, directory, "add", "README.md")
	runTestGit(t, directory, "-c", "user.name=Test", "-c", "user.email=test@example.invalid",
		"-c", "commit.gpgsign=false", "commit", "-m", "readme")
	if err := os.WriteFile(outside, []byte("new\n"), 0600); err != nil {
		t.Fatal(err)
	}
	outsidePatch := []byte(runTestGit(t, directory, "diff", "--", "README.md") + "\n")
	runTestGit(t, directory, "checkout", "--", "README.md")
	if _, err := w.ValidatePatch(ctx, outsidePatch, defaultPatchLimits()); err == nil {
		t.Fatal("accepted patch outside binding scope")
	}
	newFilePatch := []byte("diff --git a/internal/workload/new.go b/internal/workload/new.go\n" +
		"new file mode 100644\nindex 0000000..1111111\n--- /dev/null\n+++ b/internal/workload/new.go\n" +
		"@@ -0,0 +1 @@\n+package workload\n")
	if _, err := w.ValidatePatch(ctx, newFilePatch, defaultPatchLimits()); err == nil {
		t.Fatal("accepted new file creation")
	}
	if _, err := w.ValidatePatch(ctx, []byte("GIT binary patch\n"), defaultPatchLimits()); err == nil {
		t.Fatal("accepted binary patch")
	}
	secretPatch := []byte("diff --git a/internal/workload/worker.go b/internal/workload/worker.go\n" +
		"--- a/internal/workload/worker.go\n+++ b/internal/workload/worker.go\n" +
		"@@ -1,2 +1,2 @@\n package workload\n-func old() {}\n+api_key=very-secret-value\n")
	if _, err := w.ValidatePatch(ctx, secretPatch, defaultPatchLimits()); err == nil {
		t.Fatal("accepted patch containing a credential-like value")
	}
}

func TestPatchRejectsChangedLineBudget(t *testing.T) {
	w, directory, ctx := patchWorkspace(t)
	file := filepath.Join(directory, "internal", "workload", "worker.go")
	if err := os.WriteFile(file, []byte("package workload\nfunc fixed() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	patch := []byte(runTestGit(t, directory, "diff", "--", "internal/workload/worker.go") + "\n")
	runTestGit(t, directory, "checkout", "--", "internal/workload/worker.go")
	limits := defaultPatchLimits()
	limits.MaxChangedLines = 1
	if _, err := w.ValidatePatch(ctx, patch, limits); err == nil {
		t.Fatal("accepted a patch exceeding its line budget")
	}
}
