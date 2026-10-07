package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveTestRecipeUsesFixedRegisteredArguments(t *testing.T) {
	binding := testBinding()
	recipe, err := ResolveTestRecipe(binding, "go-test-workload")
	if err != nil {
		t.Fatal(err)
	}
	if recipe.Executable != "/usr/local/go/bin/go" || len(recipe.Arguments) != 5 || recipe.Arguments[2] != "repair_regression" || recipe.Timeout <= 0 {
		t.Fatalf("unexpected workload recipe: %+v", recipe)
	}
	recipe.Arguments[0] = "curl"
	again, err := ResolveTestRecipe(binding, "go-test-workload")
	if err != nil || again.Arguments[0] != "test" {
		t.Fatalf("recipe registry was mutated: %+v err=%v", again, err)
	}
	for _, id := range []string{"", "go-test-all", "go-test-workload;curl evil.test", "../../bin/sh"} {
		if _, err := ResolveTestRecipe(binding, id); err == nil {
			t.Errorf("accepted unregistered or injected recipe ID %q", id)
		}
	}
	unknown := binding
	unknown.TestRecipes = []string{"future-recipe"}
	if _, err := ResolveTestRecipe(unknown, "future-recipe"); err == nil {
		t.Fatal("accepted recipe ID without a fixed implementation")
	}
	disabled := binding
	disabled.Enabled = false
	if _, err := ResolveTestRecipe(disabled, "go-test-workload"); err == nil {
		t.Fatal("accepted recipe on a disabled repository binding")
	}
}

func TestRegisteredRecipeCatalogRejectsCommandAndWildcardConfiguration(t *testing.T) {
	valid := GoTestRecipeSpec{ID: "go-test-parser", Version: "parser-v1", PackagePath: "./internal/parser",
		ExpectedTestName: "TestParser", ExpectedFailure: "parse regression", Timeout: time.Minute, MaxOutputBytes: 1024}
	for _, change := range []func(*GoTestRecipeSpec){
		func(s *GoTestRecipeSpec) { s.PackagePath = "./..." },
		func(s *GoTestRecipeSpec) { s.PackagePath = "../../private" },
		func(s *GoTestRecipeSpec) { s.PackagePath = "-exec=curl" },
		func(s *GoTestRecipeSpec) { s.BuildTags = "regression -exec=sh" },
		func(s *GoTestRecipeSpec) { s.ExpectedTestName = "TestParser;curl" },
		func(s *GoTestRecipeSpec) { s.MaxOutputBytes = 1024 * 1024 },
	} {
		candidate := valid
		change(&candidate)
		if _, err := NewRecipeCatalog([]GoTestRecipeSpec{candidate}); err == nil {
			t.Errorf("accepted unsafe trusted recipe %+v", candidate)
		}
	}
	if _, err := NewRecipeCatalog([]GoTestRecipeSpec{valid, valid}); err == nil {
		t.Fatal("duplicate recipe ID accepted")
	}
	catalog, err := NewRecipeCatalog([]GoTestRecipeSpec{valid})
	if err != nil {
		t.Fatal(err)
	}
	binding := testBinding()
	binding.TestRecipes = []string{valid.ID}
	resolved, err := catalog.Resolve(binding, valid.ID)
	if err != nil || resolved.Executable != "/usr/local/go/bin/go" || resolved.Version != "parser-v1" || strings.Join(resolved.Arguments, " ") != "test ./internal/parser -count=1" {
		t.Fatalf("generic registered recipe %+v %v", resolved, err)
	}
}

func TestSecondRegisteredRecipeRequiresBindingAuthorization(t *testing.T) {
	binding := testBinding()
	if _, err := ResolveTestRecipe(binding, "go-test-positive-int"); err == nil {
		t.Fatal("second recipe escaped approved binding")
	}
	binding.TestRecipes = []string{"go-test-positive-int"}
	recipe, err := ResolveTestRecipe(binding, "go-test-positive-int")
	if err != nil || recipe.ExpectedTestName != "TestParsePositiveAcceptsOne" || recipe.Arguments[1] != "./testdata/code-repair/positive-int" {
		t.Fatalf("second recipe %+v %v", recipe, err)
	}
}

func TestRegisteredPositiveIntegerRecipeRedGreenIntegration(t *testing.T) {
	image := os.Getenv("TEST_REPAIR_SANDBOX_IMAGE")
	if image == "" {
		t.Skip("TEST_REPAIR_SANDBOX_IMAGE is not configured")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git executable is unavailable")
	}
	root := t.TempDir()
	// The read-only fixture mount must be readable by Docker's non-root user.
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	fixturePath := "testdata/code-repair/positive-int"
	directory := filepath.Join(root, filepath.FromSlash(fixturePath))
	if err := os.MkdirAll(directory, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"parser.go", "parser_test.go"} {
		content, err := os.ReadFile(filepath.Join("..", "..", "..", filepath.FromSlash(fixturePath), name))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(directory, name), content, 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.invalid/positive-int-fixture\n\ngo 1.24.0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, root, "init", "-b", "main")
	runTestGit(t, root, "config", "core.autocrlf", "false")
	runTestGit(t, root, "add", ".")
	runTestGit(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "fixture")
	binding := testBinding()
	binding.AllowedPaths = []string{fixturePath}
	binding.TestRecipes = []string{"go-test-positive-int"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	tester := DockerTester{Image: image}
	before, err := tester.Run(ctx, root, binding, "go-test-positive-int")
	if err != nil || before.ExitCode != 1 || before.TimedOut || before.Truncated || !strings.Contains(before.Output, "TestParsePositiveAcceptsOne") || !strings.Contains(before.Output, "positive-int fixture: one rejected") {
		t.Fatalf("second fixture did not reproduce red test: %+v %v", before, err)
	}
	source := filepath.Join(directory, "parser.go")
	content, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	fixed := strings.Replace(string(content), "if value <= 1 {", "if value <= 0 {", 1)
	if fixed == string(content) {
		t.Fatal("fixture boundary moved")
	}
	if err = os.WriteFile(source, []byte(fixed), 0600); err != nil {
		t.Fatal(err)
	}
	patch := runTestGit(t, root, "diff", "--", fixturePath+"/parser.go") + "\n"
	runTestGit(t, root, "checkout", "--", fixturePath+"/parser.go")
	workspace, err := Open(root, binding, Limits{MaxFileBytes: 32 * 1024, MaxTotalBytes: 200 * 1024, MaxListedFiles: 20, MaxSearchHits: 20})
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	if _, err = workspace.ApplyPatch(ctx, []byte(patch), defaultPatchLimits()); err != nil {
		t.Fatal(err)
	}
	after, err := tester.Run(ctx, root, binding, "go-test-positive-int")
	if err != nil || after.ExitCode != 0 || after.TimedOut || after.Truncated {
		t.Fatalf("second fixture did not turn green: %+v %v", after, err)
	}
}
