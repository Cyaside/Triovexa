package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRestoreCandidateOnlyWritesOwnedValidatedSource(t *testing.T) {
	w, root, ctx := patchWorkspace(t)
	base := runTestGit(t, root, "rev-parse", "HEAD")
	runTestGit(t, root, "checkout", "--detach", base)
	if err := markPrivateCheckout(root, base); err != nil {
		t.Fatal(err)
	}
	name := "internal/workload/worker.go"
	file := filepath.Join(root, filepath.FromSlash(name))
	original, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(file, []byte(strings.Replace(string(original), "old()", "fixed()", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	patch := []byte(runTestGit(t, root, "diff", "--", name) + "\n")
	runTestGit(t, root, "checkout", "--", name)
	report, err := w.ApplyPatch(ctx, patch, defaultPatchLimits())
	if err != nil {
		t.Fatal(err)
	}
	if err = w.RestoreCandidate(ctx, base, report); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(file)
	if err != nil || string(restored) != string(original) {
		t.Fatal("restored source differs from approved Git blob", err)
	}
	if status := runTestGit(t, root, "status", "--porcelain", "--untracked-files=all"); status != "" {
		t.Fatal("restored checkout is dirty", status)
	}
}

func TestRestoreCandidateRejectsDeveloperCheckoutAndUnexpectedChanges(t *testing.T) {
	for _, scenario := range []string{"missing-marker", "attached-head", "wrong-base", "untracked-file", "ignored-file", "staged-file", "deleted-file", "out-of-scope-report"} {
		t.Run(scenario, func(t *testing.T) {
			w, root, ctx := patchWorkspace(t)
			base := runTestGit(t, root, "rev-parse", "HEAD")
			if scenario != "attached-head" {
				runTestGit(t, root, "checkout", "--detach", base)
			}
			if scenario != "missing-marker" {
				if err := markPrivateCheckout(root, base); err != nil {
					t.Fatal(err)
				}
			}
			name := "internal/workload/worker.go"
			file := filepath.Join(root, filepath.FromSlash(name))
			changed := "package workload\nfunc changed() {}\n"
			if err := os.WriteFile(file, []byte(changed), 0600); err != nil {
				t.Fatal(err)
			}
			report := PatchReport{Files: []string{name}}
			requestedBase := base
			switch scenario {
			case "wrong-base":
				requestedBase = strings.Repeat("a", 40)
			case "untracked-file":
				if err := os.WriteFile(filepath.Join(root, "unexpected.go"), []byte("package unexpected\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "staged-file":
				runTestGit(t, root, "add", name)
			case "ignored-file":
				if err := os.WriteFile(filepath.Join(root, ".git", "info", "exclude"), []byte("ignored.go\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "ignored.go"), []byte("package ignored\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "deleted-file":
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
			case "out-of-scope-report":
				report.Files = []string{"README.md"}
			}
			if err := w.RestoreCandidate(ctx, requestedBase, report); err == nil {
				t.Fatal("unsafe restore succeeded")
			}
			if scenario != "deleted-file" {
				current, err := os.ReadFile(file)
				if err != nil || string(current) != changed {
					t.Fatal("rejected restore mutated source", err)
				}
			}
		})
	}
}
