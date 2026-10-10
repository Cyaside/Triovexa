package sandbox

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCheckoutPreservesCommittedBytesDespiteRepositoryAttributes(t *testing.T) {
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, source, "init", "-b", "main")
	runTestGit(t, source, "config", "core.autocrlf", "false")
	content := []byte("original\r\n$Id$\r\n")
	if err := os.WriteFile(filepath.Join(source, "source.txt"), content, 0600); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, source, "add", "source.txt")
	// Introduce attributes after staging the CRLF blob, as in older repositories.
	if err := os.WriteFile(filepath.Join(source, ".gitattributes"), []byte("*.txt text eol=lf ident filter=fixture working-tree-encoding=UTF-8\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, source, "add", ".gitattributes")
	runTestGit(t, source, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "test: retain legacy blob")
	base := runTestGit(t, source, "rev-parse", "HEAD")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	checkout, err := checkoutGitRepository(ctx, parent, source, "main", base)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(checkout, "source.txt"))
	if err != nil || !bytes.Equal(raw, content) {
		t.Fatalf("Git blob changed during checkout: %q, %v", raw, err)
	}
	if status := runTestGit(t, checkout, "status", "--porcelain", "--untracked-files=all"); status != "" {
		t.Fatalf("fresh checkout is dirty: %s", status)
	}
	// Still reject any real source modification, including whitespace changes.
	if err := os.WriteFile(filepath.Join(checkout, "source.txt"), []byte("original \r\n$Id$\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if status := runTestGit(t, checkout, "status", "--porcelain"); status == "" {
		t.Fatal("real whitespace modification was hidden")
	}
}
