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

func runTestGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func TestCheckoutPinsBranchAndCleansMismatch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git executable is unavailable")
	}
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, source, "init", "-b", "main")
	if err := os.MkdirAll(filepath.Join(source, "internal", "workload"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "internal", "workload", "worker.go"), []byte("package workload\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, source, "add", "internal/workload/worker.go")
	runTestGit(t, source, "-c", "user.name=Test", "-c", "user.email=test@example.invalid",
		"-c", "commit.gpgsign=false", "commit", "-m", "fixture")
	revision := runTestGit(t, source, "rev-parse", "HEAD")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resolved, err := resolveGitBaseRevision(ctx, source, "main")
	if err != nil || resolved != revision {
		t.Fatalf("resolved base revision=%q want=%q err=%v", resolved, revision, err)
	}
	checkout, err := checkoutGitRepository(ctx, parent, source, "main", revision)
	if err != nil {
		t.Fatal(err)
	}
	if got := runTestGit(t, checkout, "rev-parse", "HEAD"); got != revision {
		t.Fatalf("checked out %q, wanted %q", got, revision)
	}
	if branch := runTestGit(t, checkout, "branch", "--show-current"); branch != "" {
		t.Fatalf("checkout is attached to branch %q", branch)
	}
	workspace, err := Open(checkout, testBinding(), Limits{MaxFileBytes: 1024, MaxTotalBytes: 2048, MaxListedFiles: 10, MaxSearchHits: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	if _, err := workspace.ReadFile("internal/workload/worker.go"); err != nil {
		t.Fatalf("cannot read checked-out source: %v", err)
	}
	if _, err := checkoutGitRepository(ctx, parent, source, "main", strings.Repeat("f", 40)); err == nil {
		t.Fatal("accepted a base commit that does not match the registered branch")
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "repair-checkout-") && filepath.Join(parent, entry.Name(), "repo") != checkout {
			t.Fatalf("failed checkout was not cleaned: %s", entry.Name())
		}
	}
}

func TestCheckoutRejectsUnboundedOrUnregisteredRepository(t *testing.T) {
	parent := t.TempDir()
	binding := testBinding()
	if _, err := Checkout(context.Background(), parent, binding, strings.Repeat("a", 40)); err == nil {
		t.Fatal("checkout accepted a context without a deadline")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	binding.RepositoryURL = "https://user:secret@github.com/Cyaside/Triovexa"
	if _, err := Checkout(ctx, parent, binding, strings.Repeat("a", 40)); err == nil {
		t.Fatal("checkout accepted repository credentials in the URL")
	}
}
