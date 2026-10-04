package publisher

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
)

func TestLegacyInvestigationReportStillPublishable(t *testing.T) {
	root := t.TempDir()
	name := "internal/workload/legacy_fixture.go"
	original := "package workload\n\nfunc Limit() int { return 1 }\n"
	changed := "package workload\n\nfunc Limit() int { return 2 }\n"
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, name), []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "git", append([]string{"-c", "commit.gpgsign=false", "-c", "core.hooksPath=" + filepath.Join(root, ".no-hooks"), "-C", root}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("local fixture git failed: %v, %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "--quiet")
	git("add", "--", name)
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--quiet", "-m", "fixture")
	baseSHA := git("rev-parse", "HEAD")
	patch := []byte("diff --git a/" + name + " b/" + name + "\n--- a/" + name + "\n+++ b/" + name + "\n@@ -1,3 +1,3 @@\n package workload\n \n-func Limit() int { return 1 }\n+func Limit() int { return 2 }\n")
	digest := sha256.Sum256(patch)
	patchSHA := hex.EncodeToString(digest[:])
	// Old persisted attempts and reports intentionally have no runtime metadata.
	var attempt coderepair.Attempt
	if err := json.Unmarshal([]byte(`{"ID":"legacy-attempt","CaseID":"legacy-case","Number":1,"Status":"succeeded"}`), &attempt); err != nil || attempt.Runtime != nil {
		t.Fatalf("legacy attempt no longer readable: %v", err)
	}
	report, _ := json.Marshal(map[string]any{"status": coderepair.StatePatchReady, "recipe_id": "go-test-workload",
		"patch_sha256": patchSHA, "before_exit": 1, "after_exit": 0, "evidence_ids": []string{"legacy-evidence"}})
	binding := coderepair.RepositoryBinding{ID: "binding", ServiceName: "worker", Environment: "staging",
		RepositoryURL: "https://github.com/acme/worker", BaseRef: "main", AllowedPaths: []string{"internal/workload"},
		TestRecipes: []string{"go-test-workload"}, PolicyVersion: "v1", Enabled: true}
	c := coderepair.Case{ID: attempt.CaseID, IncidentID: "legacy-incident", BindingID: binding.ID, BaseSHA: baseSHA,
		DeployedSHA: baseSHA, PolicyVersion: binding.PolicyVersion, State: coderepair.StatePublishing, Version: 6}
	var err error
	c.ScopeDigest, err = coderepair.ScopeDigest(c, binding)
	if err != nil {
		t.Fatal(err)
	}
	reviewDigest, err := coderepair.PublicationDigest(c, attempt, patchSHA)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	approval := coderepair.Approval{ID: "legacy-publication-approval", CaseID: c.ID, CaseVersion: 5,
		Phase: "publication", Decision: "approved", ScopeDigest: reviewDigest, PolicyVersion: c.PolicyVersion,
		CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(14 * time.Minute)}
	p := coderepair.Publication{ID: "legacy-publication", CaseID: c.ID, AttemptID: attempt.ID, ApprovalID: approval.ID,
		OperationID: "repair-publish:legacy-case:1", BranchName: "triovexa/repair/legacy-case/1", PatchSHA256: patchSHA,
		State: coderepair.PublicationRunning, LeaseToken: "fixture-lease", LeaseUntil: now.Add(time.Minute)}
	newBlob := sha1.Sum([]byte("blob " + strconv.Itoa(len(changed)) + "\x00" + changed))
	branches := map[string]string{"main": baseSHA}
	prExists := false
	prCreates, writeChecks := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-legacy-key" {
			t.Error("missing fake GitHub credential")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		prefix := "/repos/acme/worker"
		var body any
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, prefix+"/git/ref/heads/"):
			sha, found := branches[strings.TrimPrefix(r.URL.Path, prefix+"/git/ref/heads/")]
			if !found {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			body = map[string]any{"object": map[string]string{"sha": sha}}
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, prefix+"/git/commits/"):
			sha := strings.TrimPrefix(r.URL.Path, prefix+"/git/commits/")
			tree, message := testBaseTree, "fixture base"
			if sha == testHeadSHA {
				tree, message = testPatchTree, commitMessage(p.OperationID, patchSHA)
			} else if sha != baseSHA {
				t.Errorf("unexpected commit requested: %s", sha)
			}
			body = map[string]any{"message": message, "tree": map[string]string{"sha": tree}, "parents": []any{map[string]string{"sha": baseSHA}}}
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, prefix+"/git/trees/"):
			blob := strings.Repeat("5", 40)
			if strings.HasSuffix(r.URL.Path, testPatchTree) {
				blob = hex.EncodeToString(newBlob[:])
			}
			body = map[string]any{"truncated": false, "tree": []any{map[string]string{"path": name, "mode": "100644", "type": "blob", "sha": blob}}}
		case r.Method == http.MethodGet && r.URL.Path == prefix+"/compare/"+baseSHA+"..."+testHeadSHA:
			body = map[string]any{"ahead_by": 1, "behind_by": 0, "total_commits": 1,
				"files": []any{map[string]string{"filename": name, "status": "modified", "sha": hex.EncodeToString(newBlob[:])}}}
		case r.Method == http.MethodPost && r.URL.Path == prefix+"/git/trees":
			body = map[string]string{"sha": testPatchTree}
		case r.Method == http.MethodPost && r.URL.Path == prefix+"/git/commits":
			body = map[string]string{"sha": testHeadSHA}
		case r.Method == http.MethodPost && r.URL.Path == prefix+"/git/refs":
			branches[p.BranchName] = testHeadSHA
			body = map[string]any{"object": map[string]string{"sha": testHeadSHA}}
		case r.Method == http.MethodGet && r.URL.Path == prefix+"/pulls":
			body = []any{}
			if prExists {
				body = []any{legacyPullRequest(p, baseSHA)}
			}
		case r.Method == http.MethodPost && r.URL.Path == prefix+"/pulls":
			prCreates++
			prExists = true
			var request struct{ Body string }
			if json.NewDecoder(r.Body).Decode(&request) != nil || !strings.Contains(request.Body, patchSHA) || !strings.Contains(request.Body, "go-test-workload") {
				t.Error("legacy proof did not reach the reviewed PR body")
			}
			body = legacyPullRequest(p, baseSHA)
		default:
			t.Errorf("unexpected fake GitHub route %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer server.Close()
	github, err := newGitHub(server.URL, "synthetic-legacy-key")
	if err != nil {
		t.Fatal(err)
	}
	cleanups := 0
	service, err := NewService(github, func(_ context.Context, approved coderepair.RepositoryBinding, revision string) (string, func() error, error) {
		if revision != baseSHA || approved.ID != binding.ID {
			t.Fatal("legacy publication escaped the approved repository/revision")
		}
		return root, func() error { cleanups++; return nil }, nil
	}, func(context.Context) error { writeChecks++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	pr, head, err := service.Publish(context.Background(), p, Input{Case: c, Attempt: attempt, Binding: binding,
		Approval: approval, Patch: patch, ReportJSON: report}, now)
	if err != nil || pr.Number != 7 || head != testHeadSHA || prCreates != 1 || writeChecks != 1 || cleanups != 1 {
		t.Fatalf("legacy proof was not safely publishable: pr=%+v head=%s creates=%d writes=%d cleanup=%d err=%v", pr, head, prCreates, writeChecks, cleanups, err)
	}
}

func legacyPullRequest(p coderepair.Publication, baseSHA string) map[string]any {
	return map[string]any{"number": 7, "html_url": "https://github.com/acme/worker/pull/7", "state": "open",
		"head": map[string]string{"sha": testHeadSHA, "ref": p.BranchName}, "base": map[string]string{"sha": baseSHA, "ref": "main"}}
}
