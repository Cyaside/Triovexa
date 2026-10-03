package sandbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func quotaWorkspace(t *testing.T, root string, limits Limits) *Workspace {
	t.Helper()
	if root == "" {
		root = t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "internal", "workload"), 0700); err != nil {
			t.Fatal(err)
		}
		for name, bytes := range map[string]int{"one.go": 60, "two.go": 50, "three.go": 20} {
			if err := os.WriteFile(filepath.Join(root, "internal", "workload", name), []byte(strings.Repeat("x", bytes)), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	w, err := Open(root, testBinding(), limits)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w
}

func TestSourceQuotaResumeChargesApprovedFileOnceAndBlocksNewBytes(t *testing.T) {
	limits := Limits{MaxFileBytes: 100, MaxTotalBytes: 100, MaxListedFiles: 3, MaxSearchHits: 20}
	w := quotaWorkspace(t, "", limits)
	revision := strings.Repeat("a", 40)
	first := NewSourceIndex(w, revision)
	if _, err := first.ReadRange("internal/workload/one.go", 1, 1, ""); err != nil {
		t.Fatal(err)
	}
	state := first.Accounting()
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), strings.Repeat("x", 20)) {
		t.Fatal("accounting snapshot contains source contents")
	}
	// A process restart opens the same approved checkout with a fresh physical
	// read budget. Its persisted logical unique-byte budget must not reset.
	restarted := NewSourceIndex(quotaWorkspace(t, w.RootPath(), limits), revision)
	if err := restarted.RestoreAccounting(state); err != nil {
		t.Fatal(err)
	}
	if restarted.Reads != 0 || restarted.BytesRead != 60 {
		t.Fatal("restoring quota read source or reset its bytes")
	}
	for count := 0; count < 2; count++ {
		if _, err := restarted.ReadRange("internal/workload/one.go", 1, 1, ""); err != nil {
			t.Fatal(err)
		}
	}
	if restarted.Reads != 1 || restarted.BytesRead != 60 {
		t.Fatal("same approved file charged unique bytes twice")
	}
	if _, err := restarted.ReadRange("internal/workload/two.go", 1, 1, ""); err == nil || restarted.BytesRead != 60 {
		t.Fatal("restart reset unique-byte quota")
	}
	if _, err := restarted.ReadRange("internal/workload/three.go", 1, 1, ""); err != nil || restarted.BytesRead != 80 {
		t.Fatal("remaining unique-byte allowance not preserved", err)
	}
	state = restarted.Accounting()
	fresh := NewSourceIndex(quotaWorkspace(t, w.RootPath(), limits), revision)
	if err := fresh.RestoreAccounting(state); err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.ReadRange("internal/workload/two.go", 1, 1, ""); err == nil {
		t.Fatal("second restart reset cumulative source quota")
	}
}

func TestSourceQuotaResumePreservesUniqueFileLimit(t *testing.T) {
	limits := Limits{MaxFileBytes: 100, MaxTotalBytes: 200, MaxListedFiles: 1, MaxSearchHits: 20}
	w := quotaWorkspace(t, "", limits)
	revision := strings.Repeat("a", 40)
	index := NewSourceIndex(w, revision)
	if _, err := index.ReadRange("internal/workload/one.go", 1, 1, ""); err != nil {
		t.Fatal(err)
	}
	restarted := NewSourceIndex(quotaWorkspace(t, w.RootPath(), limits), revision)
	if err := restarted.RestoreAccounting(index.Accounting()); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.ReadRange("internal/workload/two.go", 1, 1, ""); err == nil {
		t.Fatal("restart reset unique-file limit despite remaining bytes")
	}
	if _, err := restarted.ReadRange("internal/workload/one.go", 1, 1, ""); err != nil {
		t.Fatal("already-accounted file denied", err)
	}
}

func TestSourceAccountingRejectsScopeDigestSizeAndShrinkingState(t *testing.T) {
	w := quotaWorkspace(t, "", Limits{MaxFileBytes: 100, MaxTotalBytes: 100, MaxListedFiles: 3, MaxSearchHits: 20})
	index := NewSourceIndex(w, strings.Repeat("a", 40))
	if _, err := index.ReadRange("internal/workload/one.go", 1, 1, ""); err != nil {
		t.Fatal(err)
	}
	state := index.Accounting()
	for _, scenario := range []string{"revision", "protected-path", "invalid-digest", "negative-bytes", "oversized-file", "total-budget", "missing-file"} {
		t.Run(scenario, func(t *testing.T) {
			encoded, _ := json.Marshal(state)
			var bad SourceAccounting
			if err := json.Unmarshal(encoded, &bad); err != nil {
				t.Fatal(err)
			}
			charge := bad.Files["internal/workload/one.go"]
			switch scenario {
			case "revision":
				bad.Revision = strings.Repeat("b", 40)
			case "protected-path":
				delete(bad.Files, "internal/workload/one.go")
				bad.Files[".git/config"] = charge
			case "invalid-digest":
				charge.Digest = "not-a-digest"
				bad.Files["internal/workload/one.go"] = charge
			case "negative-bytes":
				charge.Bytes = -1
				bad.Files["internal/workload/one.go"] = charge
			case "oversized-file":
				charge.Bytes = 101
				bad.Files["internal/workload/one.go"] = charge
			case "total-budget":
				bad.Files["internal/workload/two.go"] = SourceCharge{Digest: strings.Repeat("a", 64), Bytes: 50}
			case "missing-file":
				delete(bad.Files, "internal/workload/one.go")
			}
			if err := index.RestoreAccounting(bad); err == nil {
				t.Fatal("invalid/shrinking accounting accepted")
			}
		})
	}
	// Restored quota contains no raw source. Lazy reread must verify the digest
	// and length before accepting an altered file under the same revision.
	restarted := NewSourceIndex(quotaWorkspace(t, w.RootPath(), w.limits), index.revision)
	if err := restarted.RestoreAccounting(state); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.RootPath(), "internal", "workload", "one.go"), []byte(strings.Repeat("y", 60)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.ReadRange("internal/workload/one.go", 1, 1, ""); err == nil {
		t.Fatal("accounted digest did not detect changed source")
	}
}
