package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cyaside/Triovexa/internal/coderepair"
)

func testBinding() coderepair.RepositoryBinding {
	return coderepair.RepositoryBinding{
		ID: "binding-1", ServiceName: "queue-worker", Environment: "staging",
		RepositoryURL: "https://github.com/Cyaside/Triovexa", BaseRef: "main",
		AllowedPaths: []string{"internal/workload"}, TestRecipes: []string{"go-test-workload"}, PolicyVersion: "repair-v1",
	}
}

func testWorkspace(t *testing.T, limits Limits) (*Workspace, string) {
	t.Helper()
	directory := t.TempDir()
	if err := os.MkdirAll(filepath.Join(directory, "internal", "workload"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "internal", "workload", "worker.go"), []byte("package workload\nfunc process() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	workspace, err := Open(directory, testBinding(), limits)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })
	return workspace, directory
}

func TestWorkspaceReadListSearchAndPathIsolation(t *testing.T) {
	w, directory := testWorkspace(t, Limits{MaxFileBytes: 1024, MaxTotalBytes: 2048, MaxListedFiles: 10, MaxSearchHits: 3})
	content, err := w.ReadFile("internal/workload/worker.go")
	if err != nil || !strings.Contains(string(content), "func process") {
		t.Fatalf("read allowed code: %q, err %v", content, err)
	}
	names, err := w.ListFiles("internal/workload")
	if err != nil || len(names) != 1 || names[0] != "internal/workload/worker.go" {
		t.Fatalf("list allowed files: %v, err %v", names, err)
	}
	hits, err := w.SearchText("internal/workload", "process")
	if err != nil || len(hits) != 1 || hits[0].Path != names[0] || hits[0].Line != 2 {
		t.Fatalf("search allowed code: %v, err %v", hits, err)
	}
	for _, name := range []string{
		"../secret.txt", "internal/workload/../../../secret.txt", "internal/workload-other/file.go",
		"internal/workload/.env", "internal/workload/secret.key", `internal\workload\worker.go`,
	} {
		if _, err := w.ReadFile(name); err == nil {
			t.Errorf("read forbidden path %q", name)
		}
	}
	outside := filepath.Join(directory, "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "internal", "workload", "linked.go")
	if err := os.Symlink(outside, link); err == nil {
		if _, err := w.ReadFile("internal/workload/linked.go"); err == nil {
			t.Fatal("followed symlink outside allowed scope")
		}
		if _, err := w.ListFiles("internal/workload"); err == nil {
			t.Fatal("listed a scope containing a symlink")
		}
	}
}

func TestWorkspaceRejectsSecretsAndBudgetOverruns(t *testing.T) {
	w, directory := testWorkspace(t, Limits{MaxFileBytes: 128, MaxTotalBytes: 128, MaxListedFiles: 1, MaxSearchHits: 1})
	secretPath := filepath.Join(directory, "internal", "workload", "credential.go")
	if err := os.WriteFile(secretPath, []byte("api_key=very-secret-value"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ReadFile("internal/workload/credential.go"); err == nil {
		t.Fatal("exposed credential-like code")
	}
	if _, err := w.ListFiles("internal/workload"); err == nil {
		t.Fatal("listing exceeded file count limit")
	}
	if err := os.WriteFile(filepath.Join(directory, "internal", "workload", "large.go"), []byte(strings.Repeat("a", 129)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ReadFile("internal/workload/large.go"); err == nil {
		t.Fatal("read file exceeding per-file limit")
	}
	for i := 0; i < 3; i++ {
		if _, err := w.ReadFile("internal/workload/worker.go"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.ReadFile("internal/workload/worker.go"); err == nil {
		t.Fatal("read exceeded total byte budget")
	}
}

func TestWorkspaceSearchStopsAtResultLimit(t *testing.T) {
	w, directory := testWorkspace(t, Limits{MaxFileBytes: 1024, MaxTotalBytes: 2048, MaxListedFiles: 10, MaxSearchHits: 1})
	code := []byte("package workload\nfunc process() {}\nfunc processAgain() {}\n")
	if err := os.WriteFile(filepath.Join(directory, "internal", "workload", "worker.go"), code, 0600); err != nil {
		t.Fatal(err)
	}
	if hits, err := w.SearchText("internal/workload", "process"); err == nil || hits != nil {
		t.Fatalf("search returned partial results after limit: hits=%v err=%v", hits, err)
	}
}
