package sandbox

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestStructuredEditsPreserveSourceRepresentation(t *testing.T) {
	for _, source := range []string{"package workload\nfunc old() {}\n", "package workload\r\nfunc old() {}\r\n", "package workload\nfunc old() {}", "hello π\tworld"} {
		t.Run(strings.ReplaceAll(source, "\r", "CR"), func(t *testing.T) {
			w, dir, ctx := patchWorkspace(t)
			name := "internal/workload/worker.go"
			if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			runTestGit(t, dir, "add", name)
			runTestGit(t, dir, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "representation")
			base := runTestGit(t, dir, "rev-parse", "HEAD")
			index := NewSourceIndex(w, base)
			read, err := index.ReadRange(name, 1, 100, "")
			if err != nil || strings.Contains(read.Text, "\r") {
				t.Fatal("logical source", err)
			}
			old, next := "old", "fixed"
			if strings.Contains(source, "π") {
				old, next = "π\tworld", "λ\tearth"
			}
			files := []FileEdits{{Path: name, ExpectedDigest: read.Digest, Edits: []TextEdit{{OldText: old, NewText: next}}}}
			patch, err := index.BuildPatch(ctx, files, defaultPatchLimits())
			if err != nil {
				t.Fatal(err)
			}
			second, err := index.BuildPatch(ctx, files, defaultPatchLimits())
			if err != nil || string(second) != string(patch) {
				t.Fatal("non-deterministic generation", err)
			}
			if status := runTestGit(t, dir, "status", "--porcelain"); status != "" {
				t.Fatal("builder changed checkout/index", status)
			}
			if _, err := w.ApplyPatch(ctx, patch, defaultPatchLimits()); err != nil {
				t.Fatal(err)
			}
			actual, _ := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
			if string(actual) != strings.Replace(source, old, next, 1) {
				t.Fatalf("changed unrelated bytes: %q", actual)
			}
		})
	}
}

func TestStructuredEditsRejectInvalidPreconditions(t *testing.T) {
	for _, name := range []string{"stale", "unread", "absent", "ambiguous", "overlap", "noop", "protected", "duplicate-file", "binary", "secret", "mixed-eol"} {
		t.Run(name, func(t *testing.T) {
			w, dir, ctx := patchWorkspace(t)
			path := "internal/workload/worker.go"
			if name == "mixed-eol" {
				if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(path)), []byte("package workload\r\nfunc old() {}\n"), 0600); err != nil {
					t.Fatal(err)
				}
				runTestGit(t, dir, "add", path)
				runTestGit(t, dir, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "mixed")
			}
			index := NewSourceIndex(w, runTestGit(t, dir, "rev-parse", "HEAD"))
			read, err := index.ReadRange(path, 1, 100, "")
			if name == "mixed-eol" {
				if err == nil {
					t.Fatal("mixed source was exposed")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			files := []FileEdits{{Path: path, ExpectedDigest: read.Digest, Edits: []TextEdit{{OldText: "old", NewText: "fixed"}}}}
			switch name {
			case "stale":
				files[0].ExpectedDigest = strings.Repeat("0", 64)
			case "unread":
				index = NewSourceIndex(w, index.revision)
			case "absent":
				files[0].Edits[0].OldText = "not-present"
			case "ambiguous":
				files[0].Edits[0].OldText = "o"
			case "overlap":
				files[0].Edits = append(files[0].Edits, TextEdit{OldText: "old()", NewText: "next()"})
			case "noop":
				files[0].Edits[0].NewText = "old"
			case "protected":
				files[0].Path = "../README.md"
			case "duplicate-file":
				files = append(files, files[0])
			case "binary":
				files[0].Edits[0].NewText = "\x00"
			case "secret":
				files[0].Edits[0].NewText = "api_key=very-secret-value"
			}
			if _, err := index.BuildPatch(ctx, files, defaultPatchLimits()); err == nil {
				t.Fatal("accepted invalid proposal")
			}
			if status := runTestGit(t, dir, "status", "--porcelain"); status != "" {
				t.Fatal("rejected edit changed checkout", status)
			}
		})
	}
}

func TestReadCoverageAndEditOrderingSurviveRestore(t *testing.T) {
	w, dir, ctx := patchWorkspace(t)
	index := NewSourceIndex(w, runTestGit(t, dir, "rev-parse", "HEAD"))
	read, err := index.ReadRange("internal/workload/worker.go", 1, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	files := []FileEdits{{Path: read.Path, ExpectedDigest: read.Digest, Edits: []TextEdit{{OldText: "old", NewText: "fixed"}}}}
	if _, err := index.BuildPatch(ctx, files, defaultPatchLimits()); err == nil {
		t.Fatal("reading header authorized unread body")
	}
	if _, err := index.ReadRange(read.Path, 2, 2, read.Digest); err != nil {
		t.Fatal(err)
	}
	state := index.Accounting()
	restored := NewSourceIndex(w, index.revision)
	if err := restored.RestoreAccounting(state); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.Accounting(), state) {
		t.Fatal("read proof changed on restore")
	}
	files[0].Edits = append(files[0].Edits, TextEdit{OldText: "workload", NewText: "workloadfixed"})
	a, err := restored.BuildPatch(ctx, files, defaultPatchLimits())
	if err != nil {
		t.Fatal(err)
	}
	files[0].Edits[0], files[0].Edits[1] = files[0].Edits[1], files[0].Edits[0]
	b, err := restored.BuildPatch(ctx, files, defaultPatchLimits())
	if err != nil || string(a) != string(b) {
		t.Fatal("proposal ordering changed canonical patch", err)
	}
	shrunk := state
	shrunk.ReadRanges = nil
	if err := restored.RestoreAccounting(shrunk); err == nil {
		t.Fatal("read provenance shrank")
	}
}
