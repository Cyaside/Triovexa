package runtimebridge

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/coderepair/sandbox"
)

func TestToolReceiptsPreserveSourceQuotaAcrossResume(t *testing.T) {
	h := newReceiptHarness(t)
	root := h.w.RootPath()
	if err := os.WriteFile(filepath.Join(root, "internal", "workload", "additional.go"), []byte("package workload\nfunc extra() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, root, "add", "internal/workload/additional.go")
	fixtureGit(t, root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "quota fixture")
	h.base = fixtureGit(t, root, "rev-parse", "HEAD")
	content, err := os.ReadFile(filepath.Join(root, "internal", "workload", "worker.go"))
	if err != nil {
		t.Fatal(err)
	}
	limits := sandbox.Limits{MaxFileBytes: int64(len(content)), MaxTotalBytes: int64(len(content)), MaxListedFiles: 20, MaxSearchHits: 20}
	newSession := func() *ToolSession {
		w, err := sandbox.Open(root, h.binding, limits)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = w.Close() })
		s, err := NewToolSession(w, h.binding, h.snapshot, coderepair.AgentSelection{Provider: "openai-compatible", Model: "fixture-model", PromptVersion: PromptVersion}, "go-test-workload", h.tests, func(ctx context.Context) error { return ctx.Err() }, DefaultLimits(), h.base)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Baseline(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := s.RestoreReceipts(context.Background(), h.store, h.claim, h.cipher); err != nil {
			t.Fatal(err)
		}
		return s
	}
	first := newSession()
	if response := first.Execute(context.Background(), sourceRead("quota-read-original")); response.Status != "ok" {
		t.Fatal(response)
	}
	if first.index.BytesRead != int64(len(content)) {
		t.Fatal("first read was not accounted")
	}
	restarted := newSession()
	if restarted.index.BytesRead != int64(len(content)) || restarted.index.Reads != 0 {
		t.Fatal("encrypted receipt reset unique-source quota or read source during restore")
	}
	// A different approved range is a fresh tool request after process resume,
	// not just replaying the old receipt. It must reuse the unique byte charge.
	request := ToolRequest{CallID: "quota-read-after-resume", Name: "repo_read", Args: json.RawMessage(`{"path":"internal/workload/worker.go","start_line":2,"end_line":4}`)}
	if response := restarted.Execute(context.Background(), request); response.Status != "ok" || restarted.index.Reads != 1 || restarted.index.BytesRead != int64(len(content)) {
		t.Fatalf("re-read charged source twice: %+v", response)
	}
	secondRestart := newSession()
	request = ToolRequest{CallID: "quota-new-file", Name: "repo_read", Args: json.RawMessage(`{"path":"internal/workload/additional.go","start_line":1,"end_line":2}`)}
	if response := secondRestart.Execute(context.Background(), request); response.Status != "error" || secondRestart.index.Reads != 0 || secondRestart.index.BytesRead != int64(len(content)) {
		t.Fatalf("process resume allowed extra source beyond cumulative quota: %+v", response)
	}
}
