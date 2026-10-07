package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/security"
)

var imageReference = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,200}$`)

// DockerTester is used by the repair worker, never by the web server or the
// model. Docker receives a fixed operation and a read-only checkout mount.
type DockerTester struct{ Image string }

func (r DockerTester) Run(ctx context.Context, checkout string, binding coderepair.RepositoryBinding, recipeID string) (TestResult, error) {
	if binding.ValidationProfile != nil {
		r.Image = binding.ValidationProfile.Image
	}
	if !imageReference.MatchString(r.Image) || strings.Contains(r.Image, "..") {
		return TestResult{}, errors.New("invalid repair sandbox image")
	}
	checkout, err := filepath.Abs(checkout)
	if err != nil {
		return TestResult{}, err
	}
	info, err := os.Lstat(checkout)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return TestResult{}, errors.New("sandbox checkout must be a real directory")
	}
	recipe, err := ResolveTestRecipe(binding, recipeID)
	if err != nil {
		return TestResult{}, err
	}
	duration := recipe.Timeout + time.Minute
	for _, check := range recipe.Checks {
		duration += time.Duration(check.TimeoutSeconds) * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	// Credentials and investigation grants have no role inside repository tests.
	testBinding := binding
	testBinding.CredentialRef, testBinding.Automation = "", nil
	request, err := json.Marshal(struct {
		Operation string                       `json:"operation"`
		RecipeID  string                       `json:"recipe_id"`
		Binding   coderepair.RepositoryBinding `json:"binding"`
	}{"run_allowed_test", recipeID, testBinding})
	if err != nil {
		return TestResult{}, err
	}
	command := exec.CommandContext(runCtx, "docker", "run", "--pull=never", "--rm", "-i", "--network", "none",
		"--read-only", "--user", "10001:10001", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "128",
		"--memory", "2g", "--cpus", "2", "--tmpfs", "/tmp:rw,exec,nosuid,nodev,size=1g",
		"--mount", "type=bind,source="+checkout+",target=/workspace,readonly", r.Image)
	command.Stdin = bytes.NewReader(request)
	output := &boundedOutput{limit: 64 * 1024}
	command.Stdout = output
	command.Stderr = output
	if err := command.Run(); err != nil {
		if runCtx.Err() != nil {
			return TestResult{}, runCtx.Err()
		}
		return TestResult{}, fmt.Errorf("isolated test runner failed: %s", security.Redact(output.String()))
	}
	if output.truncated {
		return TestResult{}, errors.New("isolated test result exceeded byte budget")
	}
	var result TestResult
	if err := json.Unmarshal([]byte(output.String()), &result); err != nil || result.RecipeID != recipeID {
		return TestResult{}, errors.New("isolated test runner returned an invalid result")
	}
	return result, nil
}
