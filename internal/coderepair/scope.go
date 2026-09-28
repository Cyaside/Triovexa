package coderepair

import (
	"errors"
	"net/url"
	"path"
	"regexp"
	"strings"
)

var (
	repositoryPart = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	recipeID       = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
	baseRef        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)
)

// ValidateRepositoryURL accepts only a canonical GitHub repository URL for
// the initial single-repository integration. Credentials, redirects and
// alternate hosts are outside the repair runner's authority.
func ValidateRepositoryURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Hostname(), "github.com") ||
		u.Port() != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return errors.New("repository must be a canonical GitHub HTTPS URL")
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) != 2 || !repositoryPart.MatchString(parts[0]) || !repositoryPart.MatchString(parts[1]) ||
		parts[0] == "." || parts[0] == ".." || parts[1] == "." || parts[1] == ".." ||
		path.Clean(u.Path) != u.Path {
		return errors.New("repository URL must identify one owner and repository")
	}
	return nil
}

func validateBaseRef(ref string) bool {
	if !baseRef.MatchString(ref) || strings.Contains(ref, "..") || strings.Contains(ref, "//") ||
		strings.HasSuffix(ref, "/") || strings.HasSuffix(ref, ".") || strings.HasSuffix(ref, ".lock") {
		return false
	}
	for _, part := range strings.Split(ref, "/") {
		if strings.HasPrefix(part, ".") || strings.HasPrefix(part, "-") {
			return false
		}
	}
	return true
}

// ValidateRepoPath rejects traversal, platform-specific paths and protected
// files before any file operation. Paths always use Git's forward slashes.
func ValidateRepoPath(name string) error {
	if name == "" || name == "." || strings.HasPrefix(name, "/") || path.Clean(name) != name ||
		strings.ContainsAny(name, "\\:\x00\r\n*?<>|") {
		return errors.New("repository path must be a clean relative path")
	}
	for _, part := range strings.Split(name, "/") {
		lower := strings.ToLower(part)
		if part == "" || part == "." || part == ".." || lower == ".git" || lower == ".github" ||
			lower == ".env" || strings.HasPrefix(lower, ".env.") || lower == "id_rsa" ||
			strings.HasSuffix(lower, ".pem") || strings.HasSuffix(lower, ".p12") || strings.HasSuffix(lower, ".key") {
			return errors.New("repository path is protected or invalid")
		}
	}
	return nil
}

func (b RepositoryBinding) AllowsPath(name string) bool {
	if ValidateRepoPath(name) != nil {
		return false
	}
	for _, prefix := range b.AllowedPaths {
		if ValidateRepoPath(prefix) == nil && (name == prefix || strings.HasPrefix(name, prefix+"/")) {
			return true
		}
	}
	return false
}

func validateRecipeID(id string) bool { return recipeID.MatchString(id) }
