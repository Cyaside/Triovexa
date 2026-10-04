package runtimebridge

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cyaside/Triovexa/internal/coderepair"
)

func TestProposalsCannotChangeUnreadSourceOrProtectedRegressionTests(t *testing.T) {
	for _, protectedTest := range []bool{false, true} {
		name := "unread source"
		if protectedTest {
			name = "protected regression test"
		}
		t.Run(name, func(t *testing.T) {
			h := newReceiptHarness(t)
			session := h.session(t, true)
			patch := h.patch
			if protectedTest {
				path := "internal/workload/worker_regression_test.go"
				session.Execute(context.Background(), ToolRequest{CallID: "read-test", Name: "repo_read", Args: json.RawMessage(`{"path":"` + path + `","start_line":1,"end_line":30}`)})
				original, err := os.ReadFile(filepath.Join(h.w.RootPath(), filepath.FromSlash(path)))
				if err != nil {
					t.Fatal(err)
				}
				changed := strings.Replace(string(original), "t.Fatal(", "t.Skip(", 1)
				if err := os.WriteFile(filepath.Join(h.w.RootPath(), filepath.FromSlash(path)), []byte(changed), 0600); err != nil {
					t.Fatal(err)
				}
				patch = fixtureGit(t, h.w.RootPath(), "diff", "--", path) + "\n"
				fixtureGit(t, h.w.RootPath(), "checkout", "--", path)
			}
			args, _ := json.Marshal(map[string]any{"patch": patch, "hypothesis": "version check rejects valid schema two jobs", "evidence_ids": []string{"log-1", "metric-1"}})
			session.Execute(context.Background(), ToolRequest{CallID: "patch", Name: "propose_patch", Args: args})
			result := session.Result()
			if result.Status != coderepair.StateFailed || result.Code != "UNGROUNDED_PATCH" || result.Patch != "" || result.PatchReport.SHA256 != "" || h.tests.calls != 1 {
				t.Fatalf("unread/protected patch became publishable: %+v", result)
			}
			if status := fixtureGit(t, h.w.RootPath(), "status", "--porcelain"); status != "" {
				t.Fatal("rejected patch changed source before its scope check")
			}
		})
	}
}

func TestUnauthorizedToolNameIsNotReturnedAsPublicDiagnostics(t *testing.T) {
	h := newReceiptHarness(t)
	session := h.session(t, true)
	const private = "private-unlabelled-tool-name-482d"
	session.Execute(context.Background(), ToolRequest{CallID: "unknown", Name: private, Args: json.RawMessage(`{}`)})
	result := session.Result()
	if result.Code != "PROVIDER_CONTRACT_INVALID" || strings.Contains(result.Reason, private) || session.index.Reads != 0 || h.tests.calls != 1 {
		t.Fatal("untrusted child tool name reached diagnostics or executed")
	}
}
