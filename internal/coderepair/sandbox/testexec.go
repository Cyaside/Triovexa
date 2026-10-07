package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/security"
)

type TestResult struct {
	RecipeID  string        `json:"recipe_id"`
	ExitCode  int           `json:"exit_code"`
	TimedOut  bool          `json:"timed_out"`
	Truncated bool          `json:"truncated"`
	Duration  time.Duration `json:"duration_ns"`
	Output    string        `json:"output"`
}

// RunAllowedTest is called inside the isolated repair sandbox. The caller
// chooses only an opaque recipe ID; executable, arguments, timeout and output
// budget come from the pinned admin profile or legacy fixture registry. A nonzero test exit is a result,
// never a successful test, and never retried as an alternate command.
func RunAllowedTest(ctx context.Context, rootPath string, binding coderepair.RepositoryBinding, recipeID string) (TestResult, error) {
	recipe, err := ResolveTestRecipe(binding, recipeID)
	if err != nil {
		return TestResult{}, err
	}
	rootPath, err = filepath.Abs(rootPath)
	if err != nil {
		return TestResult{}, err
	}
	info, err := os.Lstat(rootPath)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return TestResult{}, errors.New("test workspace must be a real directory")
	}
	rootFiles := recipe.RootFiles
	if binding.ValidationProfile == nil {
		rootFiles = []string{"go.mod"}
	}
	for _, name := range rootFiles {
		// Verify each component: a regular file reached through a symlink is not safe.
		current := rootPath
		for _, part := range strings.Split(name, "/") {
			current = filepath.Join(current, part)
			info, err := os.Lstat(current)
			if err != nil || info.Mode()&os.ModeSymlink != 0 {
				return TestResult{}, errors.New("validation root file is missing or unsafe")
			}
		}
		info, err := os.Lstat(current)
		if err != nil || !info.Mode().IsRegular() {
			return TestResult{}, errors.New("validation root file must be regular")
		}
	}
	var accumulated string
	var duration time.Duration
	for _, check := range recipe.Checks {
		step := recipe
		step.Executable, step.Arguments, step.Timeout = check.Executable, check.Arguments, time.Duration(check.TimeoutSeconds)*time.Second
		step.RequireEmptyOutput = check.RequireEmptyOutput
		result, err := executeRecipe(ctx, rootPath, step)
		if err != nil {
			return TestResult{}, err
		}
		accumulated += result.Output
		duration += result.Duration
		if len(accumulated) > recipe.MaxOutputBytes {
			result.Truncated = true
		}
		if result.ExitCode != 0 || result.Truncated || result.TimedOut {
			return result, nil
		}
	}
	result, err := executeRecipe(ctx, rootPath, recipe)
	result.Duration += duration
	result.Output = accumulated + result.Output
	if len(result.Output) > recipe.MaxOutputBytes {
		result.Output = result.Output[:recipe.MaxOutputBytes]
		result.Truncated = true
	}
	return result, err
}

func executeRecipe(ctx context.Context, rootPath string, recipe TestRecipe) (TestResult, error) {
	runCtx, cancel := context.WithTimeout(ctx, recipe.Timeout)
	defer cancel()
	command := exec.CommandContext(runCtx, recipe.Executable, recipe.Arguments...)
	command.Dir = rootPath
	command.Env = recipeEnvironment()
	output := &boundedOutput{limit: recipe.MaxOutputBytes}
	command.Stdout = output
	command.Stderr = output
	started := time.Now()
	runErr := command.Run()
	result := TestResult{RecipeID: recipe.ID, Duration: time.Since(started), Output: security.Redact(output.String()),
		Truncated: output.truncated}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		result.TimedOut = true
		result.ExitCode = -1
		return result, nil
	}
	if ctx.Err() != nil {
		return TestResult{}, ctx.Err()
	}
	if runErr == nil {
		if recipe.RequireEmptyOutput && strings.TrimSpace(result.Output) != "" {
			result.ExitCode = 1
		}
		return result, nil
	}
	var exitError *exec.ExitError
	if errors.As(runErr, &exitError) {
		result.ExitCode = exitError.ExitCode()
		return result, nil
	}
	return TestResult{}, fmt.Errorf("start registered test recipe: %s", security.Redact(runErr.Error()))
}

func recipeEnvironment() []string {
	keys := []string{"PATH", "SystemRoot", "WINDIR"}
	env := make([]string, 0, len(keys)+11)
	for _, key := range keys {
		if value := os.Getenv(key); value != "" {
			env = append(env, key+"="+value)
		}
	}
	temp := "/tmp"
	return append(env, "HOME="+temp, "GOCACHE="+filepath.Join(temp, "triovexa-repair-go-cache"),
		"GOPATH=/go", "GOMODCACHE=/go/pkg/mod", "TMPDIR=/tmp",
		"GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "GOWORK=off", "GOENV=off", "CGO_ENABLED=0",
		"GOMAXPROCS=2", "GOFLAGS=-mod=readonly -p=1", "PYTHONDONTWRITEBYTECODE=1", "PYTHONPYCACHEPREFIX=/tmp/python-cache",
		"npm_config_cache=/tmp/npm-cache", "CI=true")
}
