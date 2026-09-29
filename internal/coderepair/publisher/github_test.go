package publisher

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
)

const (
	testBaseSHA   = "1111111111111111111111111111111111111111"
	testHeadSHA   = "2222222222222222222222222222222222222222"
	testBaseTree  = "3333333333333333333333333333333333333333"
	testPatchTree = "4444444444444444444444444444444444444444"
)

func TestGitHubPublicationReconcilesAmbiguousBranchAndPRCreation(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	branches := map[string]string{"main": testBaseSHA}
	branchCreates, prCreates := 0, 0
	prExists := false
	operation := "repair-publish:case-one:1"
	patchSHA := strings.Repeat("a", 64)
	content := "package workload\n"
	blob := sha1.Sum([]byte("blob 17\x00" + content))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(401)
			return
		}
		prefix := "/repos/acme/worker"
		switch {
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, prefix+"/git/ref/heads/"):
			branch := strings.TrimPrefix(r.URL.Path, prefix+"/git/ref/heads/")
			sha, ok := branches[branch]
			if !ok {
				w.WriteHeader(404)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"sha": sha}})
		case r.Method == "GET" && r.URL.Path == prefix+"/git/commits/"+testBaseSHA:
			json.NewEncoder(w).Encode(map[string]any{"message": "base", "tree": map[string]string{"sha": testBaseTree}, "parents": []any{map[string]string{"sha": strings.Repeat("0", 40)}}})
		case r.Method == "GET" && r.URL.Path == prefix+"/git/commits/"+testHeadSHA:
			json.NewEncoder(w).Encode(map[string]any{"message": commitMessage(operation, patchSHA), "tree": map[string]string{"sha": testPatchTree}, "parents": []any{map[string]string{"sha": testBaseSHA}}})
		case r.Method == "GET" && r.URL.Path == prefix+"/git/trees/"+testBaseTree:
			json.NewEncoder(w).Encode(map[string]any{"truncated": false, "tree": []any{map[string]string{
				"path": "internal/workload/repair_fixture.go", "mode": "100644", "type": "blob", "sha": strings.Repeat("5", 40)}}})
		case r.Method == "GET" && r.URL.Path == prefix+"/git/trees/"+testPatchTree:
			json.NewEncoder(w).Encode(map[string]any{"truncated": false, "tree": []any{map[string]string{
				"path": "internal/workload/repair_fixture.go", "mode": "100644", "type": "blob", "sha": hex.EncodeToString(blob[:])}}})
		case r.Method == "GET" && r.URL.Path == prefix+"/compare/"+testBaseSHA+"..."+testHeadSHA:
			json.NewEncoder(w).Encode(map[string]any{"ahead_by": 1, "behind_by": 0, "total_commits": 1,
				"files": []any{map[string]string{"filename": "internal/workload/repair_fixture.go", "status": "modified", "sha": hex.EncodeToString(blob[:])}}})
		case r.Method == "POST" && r.URL.Path == prefix+"/git/trees":
			json.NewEncoder(w).Encode(map[string]string{"sha": testPatchTree})
		case r.Method == "POST" && r.URL.Path == prefix+"/git/commits":
			json.NewEncoder(w).Encode(map[string]string{"sha": testHeadSHA})
		case r.Method == "POST" && r.URL.Path == prefix+"/git/refs":
			branchCreates++
			branches["triovexa/repair/case-one/1"] = testHeadSHA
			w.WriteHeader(500) // GitHub committed the write before the response failed.
		case r.Method == "GET" && r.URL.Path == prefix+"/pulls":
			if !prExists {
				json.NewEncoder(w).Encode([]any{})
				return
			}
			json.NewEncoder(w).Encode([]any{map[string]any{
				"number": 7, "html_url": "https://github.com/acme/worker/pull/7", "state": "open",
				"head": map[string]string{"sha": testHeadSHA, "ref": "triovexa/repair/case-one/1"},
				"base": map[string]string{"ref": "main", "sha": testBaseSHA},
			}})
		case r.Method == "POST" && r.URL.Path == prefix+"/pulls":
			prCreates++
			prExists = true
			w.WriteHeader(500) // A timeout after PR creation must be reconciled.
		default:
			t.Errorf("unexpected GitHub request %s %s", r.Method, r.URL.String())
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	github, err := newGitHub(server.URL, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	binding := coderepair.RepositoryBinding{ID: "binding", ServiceName: "worker", Environment: "staging",
		RepositoryURL: "https://github.com/acme/worker", BaseRef: "main", AllowedPaths: []string{"internal/workload"},
		TestRecipes: []string{"go-test-workload"}, PolicyVersion: "v1", Enabled: true}
	p := coderepair.Publication{ID: "publication", CaseID: "case-one", AttemptID: "attempt-one",
		OperationID: operation, BranchName: "triovexa/repair/case-one/1", PatchSHA256: patchSHA}
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		pr, head, err := github.Ensure(ctx, binding, p, testBaseSHA,
			[]changedFile{{Path: "internal/workload/repair_fixture.go", Content: content}},
			"Repair incident", "Reviewed patch")
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if pr.Number != 7 || head != testHeadSHA {
			t.Fatalf("unexpected PR/head: %+v %s", pr, head)
		}
	}
	if branchCreates != 1 || prCreates != 1 {
		t.Fatalf("duplicate remote effects: branch=%d PR=%d", branchCreates, prCreates)
	}
	if _, _, _, err := github.Existing(context.Background(), binding, p, testBaseSHA,
		[]changedFile{{Path: "internal/workload/repair_fixture.go", Content: "unapproved\n"}}); err == nil {
		t.Fatal("reconciliation accepted an unapproved remote diff")
	}
}

func TestPublisherRejectsMissingApprovalAndChangedPatchBeforeNetwork(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(500)
	}))
	defer server.Close()
	github, err := newGitHub(server.URL, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(github, func(context.Context, coderepair.RepositoryBinding, string) (string, func() error, error) {
		t.Fatal("unapproved patch reached checkout")
		return "", nil, nil
	}, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	binding := coderepair.RepositoryBinding{ID: "binding", ServiceName: "worker", Environment: "staging",
		RepositoryURL: "https://github.com/acme/worker", BaseRef: "main", AllowedPaths: []string{"internal/workload"},
		TestRecipes: []string{"go-test-workload"}, PolicyVersion: "v1", Enabled: true}
	c := coderepair.Case{ID: "case-one", IncidentID: "incident-one", BindingID: binding.ID,
		BaseSHA: testBaseSHA, DeployedSHA: testBaseSHA, PolicyVersion: binding.PolicyVersion,
		State: coderepair.StatePublishing, Version: 6}
	c.ScopeDigest, err = coderepair.ScopeDigest(c, binding)
	if err != nil {
		t.Fatal(err)
	}
	patch := []byte("reviewed patch")
	digest := sha256.Sum256(patch)
	patchSHA := hex.EncodeToString(digest[:])
	attempt := coderepair.Attempt{ID: "attempt-one", CaseID: c.ID, Number: 1, Status: "succeeded"}
	reviewDigest, err := coderepair.PublicationDigest(c, attempt, patchSHA)
	if err != nil {
		t.Fatal(err)
	}
	approval := coderepair.Approval{ID: "approval-one", CaseID: c.ID, CaseVersion: 5,
		Phase: "publication", Decision: "approved", ScopeDigest: reviewDigest, PolicyVersion: c.PolicyVersion,
		CreatedAt: time.Now().Add(-time.Minute), ExpiresAt: time.Now().Add(14 * time.Minute)}
	p := coderepair.Publication{ID: "publication-one", CaseID: c.ID, AttemptID: attempt.ID,
		ApprovalID: approval.ID, OperationID: "repair-publish:case-one:1", BranchName: "triovexa/repair/case-one/1",
		PatchSHA256: patchSHA, State: coderepair.PublicationRunning, LeaseToken: "lease", LeaseUntil: time.Now().Add(time.Minute)}
	in := Input{Case: c, Attempt: attempt, Binding: binding, Approval: approval, Patch: patch}
	missing := in
	missing.Approval.ID = ""
	if _, _, err := service.Publish(context.Background(), p, missing, time.Now()); err == nil {
		t.Fatal("missing approval accepted")
	}
	changed := in
	changed.Patch = []byte("changed patch")
	if _, _, err := service.Publish(context.Background(), p, changed, time.Now()); err == nil {
		t.Fatal("changed patch accepted")
	}
	if requests != 0 {
		t.Fatalf("unapproved patch reached GitHub: %d requests", requests)
	}
}
