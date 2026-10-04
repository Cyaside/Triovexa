package runtimebridge

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/coderepair/publisher"
)

type localGitHubFixture struct {
	mu                  sync.Mutex
	base, head          string
	baseTree, patchTree string
	baseBlob, patchBlob string
	commitMessage       string
	branchExists        bool
	prExists            bool
	branchWrites        int
	prWrites            int
	requests            int
}

type loopbackGitHubTransport struct {
	root      *url.URL
	transport *http.Transport
}

func (transport loopbackGitHubTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme != "https" || request.URL.Host != "api.github.com" || (request.Method != http.MethodGet && request.Method != http.MethodPost) {
		return nil, errors.New("fixture denied an unexpected external route")
	}
	copy := request.Clone(request.Context())
	copy.URL.Scheme, copy.URL.Host = transport.root.Scheme, transport.root.Host
	copy.Host = transport.root.Host
	return transport.transport.RoundTrip(copy)
}

func gitBlob(content string) string {
	digest := sha1.Sum([]byte(fmt.Sprintf("blob %d\x00%s", len(content), content)))
	return hex.EncodeToString(digest[:])
}

func newLocalGitHubFixture(t *testing.T, binding coderepair.RepositoryBinding, publication coderepair.Publication, base, original, changed string) (*publisher.GitHub, *localGitHubFixture) {
	t.Helper()
	head := sha1.Sum([]byte("synthetic-head:" + publication.OperationID))
	fixture := &localGitHubFixture{base: base, head: hex.EncodeToString(head[:]), baseTree: strings.Repeat("3", 40), patchTree: strings.Repeat("4", 40),
		baseBlob: gitBlob(original), patchBlob: gitBlob(changed),
		commitMessage: "Repair incident (Triovexa)\n\nOperation: " + publication.OperationID + "\nPatch-SHA256: " + publication.PatchSHA256}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		fixture.requests++
		if request.Header.Get("Authorization") != "Bearer synthetic-github-fixture-key" {
			t.Error("publisher did not use the synthetic fixture credential")
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		prefix := "/repos/" + strings.TrimPrefix(binding.RepositoryURL, "https://github.com/")
		respond := func(value any) { _ = json.NewEncoder(writer).Encode(value) }
		switch {
		case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, prefix+"/git/ref/heads/"):
			branch := strings.TrimPrefix(request.URL.Path, prefix+"/git/ref/heads/")
			sha := fixture.base
			if branch == publication.BranchName && fixture.branchExists {
				sha = fixture.head
			} else if branch != binding.BaseRef {
				writer.WriteHeader(http.StatusNotFound)
				return
			}
			respond(map[string]any{"object": map[string]string{"sha": sha}})
		case request.Method == http.MethodGet && request.URL.Path == prefix+"/git/commits/"+fixture.base:
			respond(map[string]any{"message": "fixture base", "tree": map[string]string{"sha": fixture.baseTree}, "parents": []any{map[string]string{"sha": strings.Repeat("0", 40)}}})
		case request.Method == http.MethodGet && request.URL.Path == prefix+"/git/commits/"+fixture.head:
			respond(map[string]any{"message": fixture.commitMessage, "tree": map[string]string{"sha": fixture.patchTree}, "parents": []any{map[string]string{"sha": fixture.base}}})
		case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, prefix+"/git/trees/"):
			sha := fixture.baseBlob
			if request.URL.Path == prefix+"/git/trees/"+fixture.patchTree {
				sha = fixture.patchBlob
			} else if request.URL.Path != prefix+"/git/trees/"+fixture.baseTree {
				t.Error("publisher read an unplanned tree")
				writer.WriteHeader(http.StatusNotFound)
				return
			}
			respond(map[string]any{"truncated": false, "tree": []any{map[string]string{"path": "internal/workload/worker.go", "mode": "100644", "type": "blob", "sha": sha}}})
		case request.Method == http.MethodGet && request.URL.Path == prefix+"/compare/"+fixture.base+"..."+fixture.head:
			respond(map[string]any{"ahead_by": 1, "behind_by": 0, "total_commits": 1,
				"files": []any{map[string]string{"filename": "internal/workload/worker.go", "status": "modified", "sha": fixture.patchBlob}}})
		case request.Method == http.MethodPost && request.URL.Path == prefix+"/git/trees":
			var body struct {
				BaseTree string                                       `json:"base_tree"`
				Tree     []struct{ Path, Mode, Type, Content string } `json:"tree"`
			}
			if json.NewDecoder(request.Body).Decode(&body) != nil || body.BaseTree != fixture.baseTree || len(body.Tree) != 1 || body.Tree[0].Path != "internal/workload/worker.go" || body.Tree[0].Content != changed || body.Tree[0].Mode != "100644" || body.Tree[0].Type != "blob" {
				t.Error("native approved patch did not become the exact published tree")
			}
			respond(map[string]string{"sha": fixture.patchTree})
		case request.Method == http.MethodPost && request.URL.Path == prefix+"/git/commits":
			var body struct {
				Message, Tree string
				Parents       []string
			}
			if json.NewDecoder(request.Body).Decode(&body) != nil || body.Message != fixture.commitMessage || body.Tree != fixture.patchTree || len(body.Parents) != 1 || body.Parents[0] != fixture.base {
				t.Error("native publication changed commit provenance")
			}
			respond(map[string]string{"sha": fixture.head})
		case request.Method == http.MethodPost && request.URL.Path == prefix+"/git/refs":
			var body struct{ Ref, SHA string }
			if json.NewDecoder(request.Body).Decode(&body) != nil || body.Ref != "refs/heads/"+publication.BranchName || body.SHA != fixture.head {
				t.Error("publisher wrote an unapproved branch")
			}
			fixture.branchWrites++
			fixture.branchExists = true
			writer.WriteHeader(http.StatusInternalServerError) // Effect committed; acknowledgement lost.
		case request.Method == http.MethodGet && request.URL.Path == prefix+"/pulls":
			if !fixture.prExists {
				respond([]any{})
				return
			}
			respond([]any{map[string]any{"number": 7, "html_url": "https://github.com/Cyaside/Triovexa/pull/7", "state": "open",
				"head": map[string]string{"sha": fixture.head, "ref": publication.BranchName}, "base": map[string]string{"ref": binding.BaseRef, "sha": fixture.base}}})
		case request.Method == http.MethodPost && request.URL.Path == prefix+"/pulls":
			var body struct {
				Head, Base string
				Draft      bool
			}
			if json.NewDecoder(request.Body).Decode(&body) != nil || body.Head != publication.BranchName || body.Base != binding.BaseRef || !body.Draft {
				t.Error("publisher did not create the approved draft PR")
			}
			fixture.prWrites++
			fixture.prExists = true
			writer.WriteHeader(http.StatusInternalServerError) // Duplicate write must be reconciled.
		default:
			t.Errorf("unplanned fake GitHub route: %s %s", request.Method, request.URL.Path)
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	root, _ := url.Parse(server.URL)
	transport := &http.Transport{Proxy: nil}
	t.Cleanup(transport.CloseIdleConnections)
	github, err := publisher.NewGitHubWithTransport("synthetic-github-fixture-key", loopbackGitHubTransport{root: root, transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	return github, fixture
}
