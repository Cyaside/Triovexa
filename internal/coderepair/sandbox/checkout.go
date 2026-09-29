package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/security"
)

// Checkout creates a private, detached checkout of the registered branch at
// the exact approved base commit. A branch movement blocks the investigation.
func ResolveBaseRevision(ctx context.Context, binding coderepair.RepositoryBinding) (string, error) {
	if err := binding.Validate(); err != nil {
		return "", err
	}
	if _, bounded := ctx.Deadline(); !bounded {
		return "", errors.New("base revision lookup requires a deadline")
	}
	return resolveGitBaseRevision(ctx, binding.RepositoryURL, binding.BaseRef)
}

func resolveGitBaseRevision(ctx context.Context, repositoryURL, baseRef string) (string, error) {
	output, err := git(ctx, "-c", "http.followRedirects=false", "ls-remote", "--heads", repositoryURL, "refs/heads/"+baseRef)
	if err != nil {
		return "", err
	}
	parts := strings.Split(strings.TrimSpace(output), "\t")
	if len(parts) != 2 || parts[1] != "refs/heads/"+baseRef || !coderepair.ValidGitRevision(parts[0]) {
		return "", errors.New("registered base branch has no unique complete revision")
	}
	return parts[0], nil
}

func Checkout(ctx context.Context, parent string, binding coderepair.RepositoryBinding, baseSHA string) (string, error) {
	if err := binding.Validate(); err != nil {
		return "", err
	}
	if !coderepair.ValidGitRevision(baseSHA) {
		return "", errors.New("checkout requires a complete Git commit ID")
	}
	if _, bounded := ctx.Deadline(); !bounded {
		return "", errors.New("checkout requires a deadline")
	}
	return checkoutGitRepository(ctx, parent, binding.RepositoryURL, binding.BaseRef, baseSHA)
}

func checkoutGitRepository(ctx context.Context, parent, repositoryURL, baseRef, baseSHA string) (string, error) {
	parentAbs, err := filepath.Abs(parent)
	if err != nil {
		return "", err
	}
	if info, err := os.Lstat(parentAbs); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("checkout parent must be a real directory")
	}
	directory, err := os.MkdirTemp(parentAbs, "repair-checkout-")
	if err != nil {
		return "", err
	}
	keep := false
	defer func() {
		if !keep && childOf(parentAbs, directory) {
			_ = os.RemoveAll(directory)
		}
	}()
	checkoutPath := filepath.Join(directory, "repo")
	hooksPath := filepath.Join(directory, "empty-hooks")
	if err := os.Mkdir(hooksPath, 0700); err != nil {
		return "", err
	}
	if _, err := git(ctx, "-c", "http.followRedirects=false", "clone", "--quiet", "--no-checkout", "--no-tags", "--depth=1", "--single-branch", "--branch", baseRef,
		"--", repositoryURL, checkoutPath); err != nil {
		return "", err
	}
	if _, err := git(ctx, "-C", checkoutPath, "config", "--local", "core.autocrlf", "false"); err != nil {
		return "", err
	}
	actual, err := git(ctx, "-C", checkoutPath, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(actual) != baseSHA {
		return "", errors.New("registered branch moved or base revision does not match")
	}
	if _, err := git(ctx, "-C", checkoutPath, "-c", "core.hooksPath="+hooksPath,
		"checkout", "--quiet", "--detach", baseSHA); err != nil {
		return "", err
	}
	status, err := git(ctx, "-C", checkoutPath, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(status) != "" {
		return "", errors.New("checkout is not clean")
	}
	keep = true
	return checkoutPath, nil
}

func childOf(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func git(ctx context.Context, args ...string) (string, error) {
	return gitInput(ctx, nil, args...)
}

func gitInput(ctx context.Context, input []byte, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", args...)
	if input != nil {
		command.Stdin = bytes.NewReader(input)
	}
	command.Env = []string{}
	for _, key := range []string{"PATH", "SystemRoot", "WINDIR", "TMP", "TEMP", "TMPDIR"} {
		if value := os.Getenv(key); value != "" {
			command.Env = append(command.Env, key+"="+value)
		}
	}
	command.Env = append(command.Env,
		"GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	output := &boundedOutput{limit: 4096}
	command.Stdout = output
	command.Stderr = output
	if err := command.Run(); err != nil {
		message := security.Redact(output.String())
		return "", fmt.Errorf("git operation failed: %w: %s", err, message)
	}
	if output.truncated {
		return "", errors.New("git output exceeded limit")
	}
	return output.String(), nil
}

type boundedOutput struct {
	mu        sync.Mutex
	data      []byte
	limit     int
	truncated bool
}

func (b *boundedOutput) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - len(b.data)
	if len(data) > remaining {
		b.truncated = true
	}
	if remaining > 0 {
		if len(data) < remaining {
			remaining = len(data)
		}
		b.data = append(b.data, data[:remaining]...)
	}
	return len(data), nil
}

func (b *boundedOutput) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}
