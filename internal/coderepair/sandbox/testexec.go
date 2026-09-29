package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
// budget come from the built-in registry. A nonzero test exit is a result,
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
	module, err := os.Lstat(filepath.Join(rootPath, "go.mod"))
	if err != nil || !module.Mode().IsRegular() || module.Mode()&os.ModeSymlink != 0 {
		return TestResult{}, errors.New("test workspace has no regular Go module")
	}
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
		"GOMAXPROCS=2", "GOFLAGS=-mod=readonly -p=1")
}
