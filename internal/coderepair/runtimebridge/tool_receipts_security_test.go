package runtimebridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/coderepair/agent"
	"github.com/Cyaside/Triovexa/internal/coderepair/sandbox"
	"github.com/Cyaside/Triovexa/internal/secretstore"
)

// Only the wrapper semantics use this fixture. SQL transactions, crash
// persistence and lease fencing are exercised by PostgreSQL integration tests.
type toolReceiptFixtureStore struct {
	rows                 []coderepair.ToolReceipt
	starts, completions  int
	listErr, completeErr error
}

func (s *toolReceiptFixtureStore) StartRepairTool(_ context.Context, _ coderepair.ToolReceiptClaim, receipt coderepair.ToolReceipt) (coderepair.ToolReceipt, error) {
	s.starts++
	for _, previous := range s.rows {
		if previous.AttemptID == receipt.AttemptID && previous.CallID == receipt.CallID {
			return previous, coderepair.ErrToolReceiptMismatch
		}
	}
	s.rows = append(s.rows, receipt)
	return receipt, nil
}

func (s *toolReceiptFixtureStore) CompleteRepairTool(_ context.Context, _ coderepair.ToolReceiptClaim, receipt coderepair.ToolReceipt) error {
	s.completions++
	if s.completeErr != nil {
		return s.completeErr
	}
	for index, row := range s.rows {
		if row.AttemptID == receipt.AttemptID && row.CallID == receipt.CallID {
			receipt.State = "completed"
			s.rows[index] = receipt
			return nil
		}
	}
	return coderepair.ErrToolReceiptMissing
}

func (s *toolReceiptFixtureStore) ListRepairToolReceipts(_ context.Context, _ coderepair.ToolReceiptClaim) ([]coderepair.ToolReceipt, error) {
	return append([]coderepair.ToolReceipt(nil), s.rows...), s.listErr
}

type receiptHarness struct {
	w        *sandbox.Workspace
	binding  coderepair.RepositoryBinding
	snapshot coderepair.EvidenceSnapshot
	base     string
	patch    string
	store    *toolReceiptFixtureStore
	cipher   *secretstore.Cipher
	claim    agent.ClaimedInvestigation
	tests    *sourceTester
}

func newReceiptHarness(t *testing.T) *receiptHarness {
	t.Helper()
	w, binding, snapshot, base, patch := nativeFixture(t)
	cipher, err := secretstore.NewCipher("", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	return &receiptHarness{w: w, binding: binding, snapshot: snapshot, base: base, patch: patch,
		store: &toolReceiptFixtureStore{}, cipher: cipher, tests: &sourceTester{},
		claim: agent.ClaimedInvestigation{Job: coderepair.Job{ID: "job", CaseID: "case", AttemptID: "attempt", LeaseToken: "lease"}, ExpectedVersion: 2}}
}

func (h *receiptHarness) session(t *testing.T, restore bool) *ToolSession {
	t.Helper()
	session, err := NewToolSession(h.w, h.binding, h.snapshot,
		coderepair.AgentSelection{Provider: "openai-compatible", Model: "fixture-model", PromptVersion: PromptVersion},
		"go-test-workload", h.tests, func(ctx context.Context) error { return ctx.Err() }, DefaultLimits(), h.base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Baseline(context.Background()); err != nil {
		t.Fatal(err)
	}
	if restore {
		if err := session.RestoreReceipts(context.Background(), h.store, h.claim, h.cipher); err != nil {
			t.Fatal(err)
		}
	}
	return session
}

func sourceRead(callID string) ToolRequest {
	return ToolRequest{CallID: callID, Name: "repo_read", Args: json.RawMessage(`{"path":"internal/workload/worker.go","start_line":1,"end_line":4}`)}
}

func (h *receiptHarness) propose(t *testing.T, session *ToolSession) {
	t.Helper()
	args, _ := json.Marshal(map[string]any{"patch": h.patch, "hypothesis": "version check rejects valid schema two jobs", "evidence_ids": []string{"log-1", "metric-1"}})
	response := session.Execute(context.Background(), ToolRequest{CallID: "patch-1", Name: "propose_patch", Args: args})
	if response.Status != "ok" || session.Result().Status != coderepair.StatePatchReady {
		t.Fatalf("fixture candidate failed before receipt test: response=%+v result=%+v", response, session.Result())
	}
}

func TestCompletedSourceReceiptReplaysOnceWithoutSideEffects(t *testing.T) {
	h := newReceiptHarness(t)
	first := h.session(t, true)
	response := first.Execute(context.Background(), sourceRead("read-1"))
	if response.Status != "ok" || first.index.Reads != 1 || h.store.starts != 1 || h.store.completions != 1 {
		t.Fatalf("source receipt was not committed: %+v", response)
	}
	restarted := h.session(t, true)
	beforeRuns := h.tests.calls
	replayed := restarted.Execute(context.Background(), sourceRead("read-1"))
	a, _ := json.Marshal(response)
	b, _ := json.Marshal(replayed)
	if string(a) != string(b) || restarted.index.Reads != 0 || h.store.starts != 1 || h.store.completions != 1 || h.tests.calls != beforeRuns {
		t.Fatal("completed replay reran source/test tool or altered response")
	}
	restarted.Execute(context.Background(), sourceRead("read-1"))
	if restarted.Result().Code != "PROVIDER_CONTRACT_INVALID" || restarted.index.Reads != 0 {
		t.Fatal("duplicate tool ID was silently reused twice in one process")
	}
}

func TestSourceProvenanceUsesCheckoutBaseRatherThanDeployedRevision(t *testing.T) {
	h := newReceiptHarness(t)
	session := h.session(t, true)
	response := session.Execute(context.Background(), sourceRead("read-1"))
	encoded, _ := json.Marshal(response.Value)
	if !strings.Contains(string(encoded), h.base) || strings.Contains(string(encoded), h.snapshot.DeployedRevision) {
		t.Fatal("source provenance incorrectly described deployed code instead of checkout base")
	}
}

func TestSourceReceiptReplayRejectsChangedNameOrArguments(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		modify func(*ToolRequest)
	}{
		{"name", func(request *ToolRequest) { request.Name = "repo_search" }},
		{"range", func(request *ToolRequest) {
			request.Args = json.RawMessage(`{"path":"internal/workload/worker.go","start_line":2,"end_line":4}`)
		}},
		{"trailing JSON", func(request *ToolRequest) { request.Args = append(request.Args, []byte(`{}`)...) }},
		{"duplicate field", func(request *ToolRequest) {
			request.Args = json.RawMessage(`{"path":"private","path":"internal/workload/worker.go","start_line":1,"end_line":4}`)
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			h := newReceiptHarness(t)
			h.session(t, true).Execute(context.Background(), sourceRead("read-1"))
			restarted := h.session(t, true)
			request := sourceRead("read-1")
			scenario.modify(&request)
			restarted.Execute(context.Background(), request)
			if restarted.Result().Code != "PROVIDER_CONTRACT_INVALID" || restarted.index.Reads != 0 || h.store.starts != 1 {
				t.Fatal("changed replay argument/name reached a new tool")
			}
		})
	}
}

func TestReceiptRestoreRejectsPendingCorruptAndCrossScopeRows(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		modify func(*coderepair.ToolReceipt)
	}{
		{"pending", func(row *coderepair.ToolReceipt) { row.State = "started"; row.ResultSealed = "" }},
		{"corrupt ciphertext", func(row *coderepair.ToolReceipt) { row.ResultSealed = "v1.invalid-ciphertext" }},
		{"changed revision", func(row *coderepair.ToolReceipt) { row.Revision = strings.Repeat("b", 40) }},
		{"substituted attempt", func(row *coderepair.ToolReceipt) { row.AttemptID = "different-attempt" }},
		{"substituted tool", func(row *coderepair.ToolReceipt) { row.Name = "propose_patch" }},
		{"substituted argument digest", func(row *coderepair.ToolReceipt) { row.ArgsSHA256 = strings.Repeat("a", 64) }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			h := newReceiptHarness(t)
			h.session(t, true).Execute(context.Background(), sourceRead("read-1"))
			scenario.modify(&h.store.rows[0])
			restarted := h.session(t, false)
			if err := restarted.RestoreReceipts(context.Background(), h.store, h.claim, h.cipher); err == nil || restarted.index.Reads != 0 {
				t.Fatal("pending/corrupt/cross-scope receipt was silently restored")
			}
		})
	}
}

func TestToolReceiptFencePreventsReadEvenDuringReplay(t *testing.T) {
	h := newReceiptHarness(t)
	h.session(t, true).Execute(context.Background(), sourceRead("read-1"))
	restarted := h.session(t, true)
	restarted.fence = func(context.Context) error { return errors.New("lost lease") }
	restarted.Execute(context.Background(), sourceRead("read-1"))
	if restarted.Result().Code != "LEASE_LOST" || restarted.index.Reads != 0 || h.store.starts != 1 {
		t.Fatal("completed receipt bypassed lease fence")
	}
}

func TestUnpersistedPatchProofCannotBePublishedOrRetried(t *testing.T) {
	h := newReceiptHarness(t)
	session := h.session(t, true)
	session.Execute(context.Background(), sourceRead("read-1"))
	h.store.completeErr = errors.New("synthetic persistence failure")
	args, _ := json.Marshal(map[string]any{"patch": h.patch, "hypothesis": "version check rejects valid schema two jobs", "evidence_ids": []string{"log-1", "metric-1"}})
	session.Execute(context.Background(), ToolRequest{CallID: "patch-1", Name: "propose_patch", Args: args})
	result := session.Result()
	if result.Code != "TOOL_DISPATCH_UNCERTAIN" || result.Status != coderepair.StateBlocked || result.Patch != "" || result.PatchReport.SHA256 != "" || h.tests.calls != 2 {
		t.Fatalf("unpersisted candidate retained publishable proof: %+v", result)
	}
	if h.store.rows[1].State != "started" {
		t.Fatal("failed completion fabricated a durable terminal receipt")
	}
	// The fixture checkout is disposable; restore a new checkout at base to
	// model restart. The started dispatch must block before tools run again.
	fixtureGit(t, h.w.RootPath(), "checkout", "--", "internal/workload/worker.go")
	restarted := h.session(t, false)
	if err := restarted.RestoreReceipts(context.Background(), h.store, h.claim, h.cipher); err == nil || restarted.index.Reads != 0 {
		t.Fatal("unknown patch dispatch was silently replayed after restart")
	}
}

func TestToolArgumentDigestCanonicalizesObjectsWithoutAmbiguity(t *testing.T) {
	a, errA := toolArgumentDigest(ToolRequest{Args: json.RawMessage(`{"path":"allowed","start_line":1,"end_line":4}`)})
	b, errB := toolArgumentDigest(ToolRequest{Args: json.RawMessage(`{ "end_line":4, "start_line":1, "path":"allowed" }`)})
	if errA != nil || errB != nil || a != b {
		t.Fatalf("equivalent tool arguments got different durable identities: %s %s %v %v", a, b, errA, errB)
	}
	for _, invalid := range []string{`{"path":"allowed"}{}`, `{"path":"first","path":"second"}`} {
		if _, err := toolArgumentDigest(ToolRequest{Args: json.RawMessage(invalid)}); err == nil {
			t.Fatalf("ambiguous arguments got a replay identity: %s", invalid)
		}
	}
}

func (h *receiptHarness) resealLast(t *testing.T, modify func(*toolSnapshot)) {
	t.Helper()
	row := &h.store.rows[len(h.store.rows)-1]
	plain, err := h.cipher.Decrypt(row.ResultSealed)
	if err != nil {
		t.Fatal(err)
	}
	var state toolSnapshot
	if json.Unmarshal([]byte(plain), &state) != nil {
		t.Fatal("test fixture did not contain a tool snapshot")
	}
	modify(&state)
	encoded, _ := json.Marshal(state)
	row.ResultSealed, err = h.cipher.Encrypt(string(encoded))
	if err != nil {
		t.Fatal(err)
	}
}

func TestRestoredTerminalProofRejectsInternalDigestAndCounterCorruption(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		modify func(*toolSnapshot)
	}{
		{"patch digest", func(state *toolSnapshot) { state.Result.PatchReport.SHA256 = strings.Repeat("a", 64) }},
		{"false green", func(state *toolSnapshot) { state.Result.After.TimedOut = true }},
		{"missing candidate", func(state *toolSnapshot) { state.Candidates = 0 }},
		{"overflow steps", func(state *toolSnapshot) { state.Result.Steps = 999999 }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			h := newReceiptHarness(t)
			session := h.session(t, true)
			session.Execute(context.Background(), sourceRead("read-1"))
			h.propose(t, session)
			// Test-only resealing simulates malformed internal/old data, rather
			// than claiming an external attacker can forge authenticated data.
			h.resealLast(t, scenario.modify)
			fixtureGit(t, h.w.RootPath(), "checkout", "--", "internal/workload/worker.go")
			restarted := h.session(t, false)
			if err := restarted.RestoreReceipts(context.Background(), h.store, h.claim, h.cipher); err == nil {
				t.Fatalf("internally inconsistent terminal proof was restored: %s", scenario.name)
			}
		})
	}
}

func TestVerifiedTerminalReceiptRestoresWithoutApplyingOrTestingAgain(t *testing.T) {
	h := newReceiptHarness(t)
	session := h.session(t, true)
	session.Execute(context.Background(), sourceRead("read-1"))
	h.propose(t, session)
	verified := session.Result()
	fixtureGit(t, h.w.RootPath(), "checkout", "--", "internal/workload/worker.go")
	restarted := h.session(t, true)
	beforeRuns := h.tests.calls
	args, _ := json.Marshal(map[string]any{"patch": h.patch, "hypothesis": "version check rejects valid schema two jobs", "evidence_ids": []string{"log-1", "metric-1"}})
	response := restarted.Execute(context.Background(), ToolRequest{CallID: "patch-1", Name: "propose_patch", Args: args})
	result := restarted.Result()
	if response.Status != "ok" || result.Status != coderepair.StatePatchReady || result.Patch != verified.Patch || result.PatchReport.SHA256 != verified.PatchReport.SHA256 || h.tests.calls != beforeRuns || restarted.index.Reads != 0 || h.store.starts != 2 {
		t.Fatal("verified terminal replay lost proof or applied/retested a candidate")
	}
	if status := fixtureGit(t, h.w.RootPath(), "status", "--porcelain"); status != "" {
		t.Fatal("restoring the proof changed the base checkout")
	}
}

func TestCanonicalToolResultPreservesLargeIntegers(t *testing.T) {
	const counter int64 = 9007199254740993
	result, err := canonicalToolResult(ToolResult{CallID: "count-1", Status: "ok", Value: map[string]any{"counter": counter}})
	encoded, _ := json.Marshal(result)
	if err != nil || !strings.Contains(string(encoded), "9007199254740993") {
		t.Fatal("canonical tool response rounded integers through float64")
	}
	var restored ToolResult
	if DecodeStrict(encoded, &restored) != nil {
		t.Fatal("canonical tool response could not be restored")
	}
	roundtrip, _ := json.Marshal(restored)
	if string(roundtrip) != string(encoded) {
		t.Fatal("restored native transcript altered numeric bytes")
	}
}

func TestReceiptCiphertextCannotBeSubstitutedIntoFreshAttempt(t *testing.T) {
	h := newReceiptHarness(t)
	h.session(t, true).Execute(context.Background(), sourceRead("read-1"))
	// Ciphertext is unchanged; only unencrypted row scope and current attempt
	// are changed. Embedded sealed identity must reject cross-row copying.
	h.claim.Job.AttemptID = "fresh-attempt"
	h.store.rows[0].AttemptID = "fresh-attempt"
	restarted := h.session(t, false)
	if err := restarted.RestoreReceipts(context.Background(), h.store, h.claim, h.cipher); err == nil {
		t.Fatal("receipt ciphertext was transplanted into another attempt")
	}
}

func TestReceiptStoreFailurePreventsToolDispatch(t *testing.T) {
	h := newReceiptHarness(t)
	h.store.listErr = fmt.Errorf("synthetic storage error")
	session := h.session(t, false)
	if err := session.RestoreReceipts(context.Background(), h.store, h.claim, h.cipher); err == nil || session.index.Reads != 0 || h.store.starts != 0 {
		t.Fatal("storage failure allowed a source tool")
	}
}

func TestToolReceiptRejectsInvalidIdentifiersBeforeStorage(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		modify func(*ToolRequest)
	}{
		{"unknown tool", func(request *ToolRequest) { request.Name = "private-unlabelled-tool" }},
		{"newline call ID", func(request *ToolRequest) { request.CallID = "read\nprivate" }},
		{"whitespace call ID", func(request *ToolRequest) { request.CallID = "read private" }},
		{"oversized call ID", func(request *ToolRequest) { request.CallID = strings.Repeat("a", 129) }},
		{"empty call ID", func(request *ToolRequest) { request.CallID = "" }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			h := newReceiptHarness(t)
			session := h.session(t, true)
			request := sourceRead("read-1")
			scenario.modify(&request)
			session.Execute(context.Background(), request)
			if session.Result().Code != "PROVIDER_CONTRACT_INVALID" || session.index.Reads != 0 || h.store.starts != 0 || len(h.store.rows) != 0 {
				t.Fatal("invalid child identifier was dispatched or persisted before validation")
			}
		})
	}
}
