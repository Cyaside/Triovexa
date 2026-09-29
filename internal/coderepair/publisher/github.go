package publisher

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
)

type GitHub struct {
	baseURL string
	token   string
	client  *http.Client
}

type PullRequest struct {
	Number  int64
	URL     string
	HeadSHA string
	State   string
}

type changedFile struct {
	Path    string
	Content string
}

func NewGitHub(token string) (*GitHub, error) {
	return newGitHub("https://api.github.com", token)
}

func newGitHub(baseURL, token string) (*GitHub, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") ||
		strings.TrimSpace(token) == "" || token != strings.TrimSpace(token) {
		return nil, errors.New("GitHub publisher requires a valid API endpoint and token")
	}
	return &GitHub{baseURL: strings.TrimRight(baseURL, "/"), token: token, client: &http.Client{
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func githubRepositoryPath(binding coderepair.RepositoryBinding) (string, string, error) {
	if err := binding.Validate(); err != nil {
		return "", "", err
	}
	u, _ := url.Parse(binding.RepositoryURL)
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	return parts[0] + "/" + strings.TrimSuffix(parts[1], ".git"), parts[0], nil
}

type apiError struct{ status int }

func (e apiError) Error() string { return fmt.Sprintf("GitHub returned status %d", e.status) }

func (g *GitHub) request(ctx context.Context, method, path string, body any, target any) (int, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		if len(data) > 512*1024 {
			return 0, errors.New("GitHub request exceeds size limit")
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.baseURL+path, reader)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := g.client.Do(req)
	if err != nil {
		return 0, errors.New("GitHub request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response.StatusCode, apiError{response.StatusCode}
	}
	if target == nil {
		return response.StatusCode, nil
	}
	limited := io.LimitReader(response.Body, 1<<20)
	if err := json.NewDecoder(limited).Decode(target); err != nil {
		return 0, errors.New("GitHub returned an invalid response")
	}
	return response.StatusCode, nil
}

func (g *GitHub) branch(ctx context.Context, repo, branch string) (string, bool, error) {
	var value struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	status, err := g.request(ctx, http.MethodGet, "/repos/"+repo+"/git/ref/heads/"+branch, nil, &value)
	if status == 404 {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !coderepair.ValidGitRevision(value.Object.SHA) {
		return "", false, errors.New("GitHub branch has invalid commit ID")
	}
	return value.Object.SHA, true, nil
}

func (g *GitHub) commit(ctx context.Context, repo, sha string) (treeSHA, message, parentSHA string, err error) {
	var value struct {
		Message string `json:"message"`
		Tree    struct {
			SHA string `json:"sha"`
		} `json:"tree"`
		Parents []struct {
			SHA string `json:"sha"`
		} `json:"parents"`
	}
	_, err = g.request(ctx, http.MethodGet, "/repos/"+repo+"/git/commits/"+sha, nil, &value)
	if err != nil {
		return "", "", "", err
	}
	if len(value.Parents) != 1 || !coderepair.ValidGitRevision(value.Tree.SHA) {
		return "", "", "", errors.New("GitHub commit has unexpected ancestry")
	}
	return value.Tree.SHA, value.Message, value.Parents[0].SHA, nil
}

func (g *GitHub) baseTree(ctx context.Context, repo, baseSHA string) (string, error) {
	tree, _, _, err := g.commit(ctx, repo, baseSHA)
	return tree, err
}

func (g *GitHub) treeFiles(ctx context.Context, repo, treeSHA string, files []changedFile) (map[string]string, error) {
	var value struct {
		Truncated bool `json:"truncated"`
		Tree      []struct {
			Path string `json:"path"`
			Mode string `json:"mode"`
			Type string `json:"type"`
			SHA  string `json:"sha"`
		} `json:"tree"`
	}
	_, err := g.request(ctx, http.MethodGet, "/repos/"+repo+"/git/trees/"+treeSHA+"?recursive=1", nil, &value)
	if err != nil {
		return nil, err
	}
	if value.Truncated {
		return nil, errors.New("GitHub tree listing is truncated")
	}
	want := make(map[string]struct{}, len(files))
	for _, file := range files {
		want[file.Path] = struct{}{}
	}
	found := make(map[string]string, len(files))
	for _, entry := range value.Tree {
		if _, needed := want[entry.Path]; !needed {
			continue
		}
		if _, duplicate := found[entry.Path]; duplicate || entry.Type != "blob" || entry.Mode != "100644" ||
			!coderepair.ValidGitRevision(entry.SHA) {
			return nil, errors.New("approved file has an unexpected Git tree mode or type")
		}
		found[entry.Path] = entry.SHA
	}
	if len(found) != len(want) {
		return nil, errors.New("approved file is missing from Git tree")
	}
	return found, nil
}

func (g *GitHub) createTree(ctx context.Context, repo, baseTreeSHA string, files []changedFile) (string, error) {
	type entry struct {
		Path    string `json:"path"`
		Mode    string `json:"mode"`
		Type    string `json:"type"`
		Content string `json:"content"`
	}
	items := make([]entry, 0, len(files))
	for _, f := range files {
		items = append(items, entry{f.Path, "100644", "blob", f.Content})
	}
	var response struct {
		SHA string `json:"sha"`
	}
	_, err := g.request(ctx, http.MethodPost, "/repos/"+repo+"/git/trees", map[string]any{
		"base_tree": baseTreeSHA, "tree": items,
	}, &response)
	if err != nil {
		return "", err
	}
	if !coderepair.ValidGitRevision(response.SHA) {
		return "", errors.New("GitHub returned an invalid tree ID")
	}
	return response.SHA, nil
}

func (g *GitHub) createCommit(ctx context.Context, repo, treeSHA, baseSHA, operationID, patchSHA string) (string, error) {
	var response struct {
		SHA string `json:"sha"`
	}
	_, err := g.request(ctx, http.MethodPost, "/repos/"+repo+"/git/commits", map[string]any{
		"message": commitMessage(operationID, patchSHA), "tree": treeSHA, "parents": []string{baseSHA},
	}, &response)
	if err != nil {
		return "", err
	}
	if !coderepair.ValidGitRevision(response.SHA) {
		return "", errors.New("GitHub returned an invalid commit ID")
	}
	return response.SHA, nil
}

func commitMessage(operationID, patchSHA string) string {
	return "Repair incident (Triovexa)\n\nOperation: " + operationID + "\nPatch-SHA256: " + patchSHA
}

func (g *GitHub) createBranch(ctx context.Context, repo, branch, sha string) error {
	_, err := g.request(ctx, http.MethodPost, "/repos/"+repo+"/git/refs", map[string]string{
		"ref": "refs/heads/" + branch, "sha": sha,
	}, nil)
	return err
}

func (g *GitHub) findPR(ctx context.Context, repo, owner, branch, base, baseSHA string) (PullRequest, bool, error) {
	query := url.Values{"state": {"all"}, "head": {owner + ":" + branch}, "base": {base}, "per_page": {"100"}}
	var items []struct {
		Number  int64  `json:"number"`
		HTMLURL string `json:"html_url"`
		State   string `json:"state"`
		Head    struct {
			SHA string `json:"sha"`
			Ref string `json:"ref"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"base"`
	}
	_, err := g.request(ctx, http.MethodGet, "/repos/"+repo+"/pulls?"+query.Encode(), nil, &items)
	if err != nil {
		return PullRequest{}, false, err
	}
	if len(items) > 1 {
		return PullRequest{}, false, errors.New("multiple PRs exist for the repair branch")
	}
	if len(items) == 0 {
		return PullRequest{}, false, nil
	}
	p := items[0]
	if p.Number < 1 || p.HTMLURL == "" || p.Head.Ref != branch || p.Base.Ref != base ||
		p.Base.SHA != baseSHA || !coderepair.ValidGitRevision(p.Head.SHA) {
		return PullRequest{}, false, errors.New("GitHub returned a mismatched PR")
	}
	return PullRequest{p.Number, p.HTMLURL, p.Head.SHA, p.State}, true, nil
}

func (g *GitHub) createPR(ctx context.Context, repo, branch, base, title, body string) error {
	_, err := g.request(ctx, http.MethodPost, "/repos/"+repo+"/pulls", map[string]any{
		"title": title, "body": body, "head": branch, "base": base, "draft": true,
	}, nil)
	return err
}

// matchesApprovedFiles checks the remote commit's complete diff against the
// approved file contents. A matching operation message alone is insufficient:
// another writer could replace the branch with a different tree.
func (g *GitHub) matchesApprovedFiles(ctx context.Context, repo, baseSHA, headSHA string, files []changedFile) error {
	var comparison struct {
		AheadBy      int `json:"ahead_by"`
		BehindBy     int `json:"behind_by"`
		TotalCommits int `json:"total_commits"`
		Files        []struct {
			Filename string `json:"filename"`
			Status   string `json:"status"`
			SHA      string `json:"sha"`
		} `json:"files"`
	}
	_, err := g.request(ctx, http.MethodGet, "/repos/"+repo+"/compare/"+baseSHA+"..."+headSHA+"?per_page=100", nil, &comparison)
	if err != nil {
		return err
	}
	if comparison.AheadBy != 1 || comparison.BehindBy != 0 || comparison.TotalCommits != 1 || len(comparison.Files) != len(files) {
		return errors.New("repair branch diff does not match the approved patch")
	}
	want := make(map[string]string, len(files))
	for _, f := range files {
		blob := []byte(fmt.Sprintf("blob %d\x00%s", len(f.Content), f.Content))
		digest := sha1.Sum(blob)
		want[f.Path] = hex.EncodeToString(digest[:])
	}
	for _, f := range comparison.Files {
		if f.Status != "modified" || want[f.Filename] == "" || want[f.Filename] != f.SHA {
			return errors.New("repair branch contains an unapproved file change")
		}
		delete(want, f.Filename)
	}
	if len(want) != 0 {
		return errors.New("repair branch is missing an approved file change")
	}
	baseTree, err := g.baseTree(ctx, repo, baseSHA)
	if err != nil {
		return err
	}
	if _, err := g.treeFiles(ctx, repo, baseTree, files); err != nil {
		return err
	}
	headTree, _, _, err := g.commit(ctx, repo, headSHA)
	if err != nil {
		return err
	}
	remote, err := g.treeFiles(ctx, repo, headTree, files)
	if err != nil {
		return err
	}
	for _, file := range files {
		blob := []byte(fmt.Sprintf("blob %d\x00%s", len(file.Content), file.Content))
		digest := sha1.Sum(blob)
		if remote[file.Path] != hex.EncodeToString(digest[:]) {
			return errors.New("repair branch tree differs from the approved file")
		}
	}
	return nil
}

// Existing reconciles a previous uncertain attempt without mutating GitHub.
// The dedicated branch must point at a commit bearing this exact operation and
// patch digest, with the approved base as its sole parent.
func (g *GitHub) Existing(ctx context.Context, binding coderepair.RepositoryBinding, p coderepair.Publication, baseSHA string, files []changedFile) (PullRequest, string, bool, error) {
	repo, owner, err := githubRepositoryPath(binding)
	if err != nil {
		return PullRequest{}, "", false, err
	}
	head, exists, err := g.branch(ctx, repo, p.BranchName)
	if err != nil || !exists {
		return PullRequest{}, "", false, err
	}
	_, message, parent, err := g.commit(ctx, repo, head)
	if err != nil {
		return PullRequest{}, "", false, err
	}
	if message != commitMessage(p.OperationID, p.PatchSHA256) || parent != baseSHA {
		return PullRequest{}, "", false, errors.New("repair branch was changed or belongs to another operation")
	}
	if err := g.matchesApprovedFiles(ctx, repo, baseSHA, head, files); err != nil {
		return PullRequest{}, "", false, err
	}
	pr, found, err := g.findPR(ctx, repo, owner, p.BranchName, binding.BaseRef, baseSHA)
	if err != nil {
		return PullRequest{}, "", false, err
	}
	if found && (pr.HeadSHA != head || pr.State != "open") {
		return PullRequest{}, "", false, errors.New("repair PR head changed or PR is closed")
	}
	return pr, head, found, nil
}

// Ensure publishes only to a dedicated branch and re-reads GitHub after every
// possibly ambiguous write. The final PR is always read back, including after
// a create request reports a timeout or 422 conflict.
func (g *GitHub) Ensure(ctx context.Context, binding coderepair.RepositoryBinding, p coderepair.Publication,
	baseSHA string, files []changedFile, title, body string) (PullRequest, string, error) {
	repo, owner, err := githubRepositoryPath(binding)
	if err != nil {
		return PullRequest{}, "", err
	}
	baseHead, baseExists, err := g.branch(ctx, repo, binding.BaseRef)
	if err != nil {
		return PullRequest{}, "", err
	}
	if !baseExists || baseHead != baseSHA {
		return PullRequest{}, "", errors.New("registered base branch moved before publication")
	}
	baseTree, err := g.baseTree(ctx, repo, baseSHA)
	if err != nil {
		return PullRequest{}, "", err
	}
	if _, err := g.treeFiles(ctx, repo, baseTree, files); err != nil {
		return PullRequest{}, "", err
	}
	treeSHA, err := g.createTree(ctx, repo, baseTree, files)
	if err != nil {
		return PullRequest{}, "", err
	}
	newFiles, err := g.treeFiles(ctx, repo, treeSHA, files)
	if err != nil {
		return PullRequest{}, "", err
	}
	for _, file := range files {
		blob := []byte(fmt.Sprintf("blob %d\x00%s", len(file.Content), file.Content))
		digest := sha1.Sum(blob)
		if newFiles[file.Path] != hex.EncodeToString(digest[:]) {
			return PullRequest{}, "", errors.New("GitHub tree differs from the approved patch")
		}
	}
	head, exists, err := g.branch(ctx, repo, p.BranchName)
	if err != nil {
		return PullRequest{}, "", err
	}
	if exists {
		tree, message, parent, err := g.commit(ctx, repo, head)
		if err != nil {
			return PullRequest{}, "", err
		}
		if tree != treeSHA || message != commitMessage(p.OperationID, p.PatchSHA256) || parent != baseSHA {
			return PullRequest{}, "", errors.New("existing repair branch does not match the approved patch")
		}
	} else {
		commitSHA, err := g.createCommit(ctx, repo, treeSHA, baseSHA, p.OperationID, p.PatchSHA256)
		if err != nil {
			return PullRequest{}, "", err
		}
		createErr := g.createBranch(ctx, repo, p.BranchName, commitSHA)
		head, exists, err = g.branch(ctx, repo, p.BranchName)
		if err != nil {
			return PullRequest{}, "", err
		}
		if !exists {
			return PullRequest{}, "", createErr
		}
		tree, message, parent, err := g.commit(ctx, repo, head)
		if err != nil {
			return PullRequest{}, "", err
		}
		if tree != treeSHA || message != commitMessage(p.OperationID, p.PatchSHA256) || parent != baseSHA {
			return PullRequest{}, "", errors.New("repair branch appeared with a different commit")
		}
	}
	pr, found, err := g.findPR(ctx, repo, owner, p.BranchName, binding.BaseRef, baseSHA)
	if err != nil {
		return PullRequest{}, "", err
	}
	if !found {
		createErr := g.createPR(ctx, repo, p.BranchName, binding.BaseRef, title, body)
		pr, found, err = g.findPR(ctx, repo, owner, p.BranchName, binding.BaseRef, baseSHA)
		if err != nil {
			return PullRequest{}, "", err
		}
		if !found {
			if createErr != nil {
				return PullRequest{}, "", createErr
			}
			return PullRequest{}, "", errors.New("GitHub did not return the created PR")
		}
	}
	if pr.HeadSHA != head || pr.State != "open" {
		return PullRequest{}, "", errors.New("repair PR head changed or PR is closed")
	}
	return pr, head, nil
}
