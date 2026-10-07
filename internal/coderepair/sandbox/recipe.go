package sandbox

import (
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
)

type TestRecipe struct {
	ID                 string
	Version            string
	Executable         string
	Arguments          []string
	ExpectedTestName   string
	ExpectedFailure    string
	Timeout            time.Duration
	MaxOutputBytes     int
	Checks             []coderepair.ValidationCommand
	RootFiles          []string
	RequireEmptyOutput bool
}

// GoTestRecipeSpec is trusted operator/build configuration. It cannot select a
// shell or executable. Repository bindings and model calls select only its ID.
type GoTestRecipeSpec struct {
	ID, Version, PackagePath, BuildTags string
	ExpectedTestName, ExpectedFailure   string
	Timeout                             time.Duration
	MaxOutputBytes                      int
}

type RecipeCatalog struct{ recipes map[string]TestRecipe }

var recipeName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
var recipeTag = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)
var regressionName = regexp.MustCompile(`^Test[A-Za-z0-9_]+$`)

func NewRecipeCatalog(specs []GoTestRecipeSpec) (*RecipeCatalog, error) {
	if len(specs) == 0 || len(specs) > 32 {
		return nil, errors.New("recipe catalog requires one to 32 trusted recipes")
	}
	catalog := &RecipeCatalog{recipes: make(map[string]TestRecipe, len(specs))}
	for _, spec := range specs {
		if !recipeName.MatchString(spec.ID) || !recipeName.MatchString(spec.Version) ||
			!strings.HasPrefix(spec.PackagePath, "./") || strings.Contains(spec.PackagePath, "...") || coderepair.ValidateRepoPath(strings.TrimPrefix(spec.PackagePath, "./")) != nil ||
			(spec.BuildTags != "" && !recipeTag.MatchString(spec.BuildTags)) || !regressionName.MatchString(spec.ExpectedTestName) ||
			spec.ExpectedFailure == "" || len(spec.ExpectedFailure) > 512 || strings.ContainsAny(spec.ExpectedFailure, "\r\n\x00") ||
			spec.Timeout <= 0 || spec.Timeout > 5*time.Minute || spec.MaxOutputBytes <= 0 || spec.MaxOutputBytes > 64*1024 {
			return nil, errors.New("trusted Go test recipe exceeds catalog contract")
		}
		if _, exists := catalog.recipes[spec.ID]; exists {
			return nil, errors.New("recipe catalog contains duplicate ID")
		}
		args := []string{"test"}
		if spec.BuildTags != "" {
			args = append(args, "-tags", spec.BuildTags)
		}
		args = append(args, spec.PackagePath, "-count=1")
		catalog.recipes[spec.ID] = TestRecipe{ID: spec.ID, Version: spec.Version, Executable: "/usr/local/go/bin/go", Arguments: args,
			ExpectedTestName: spec.ExpectedTestName, ExpectedFailure: spec.ExpectedFailure, Timeout: spec.Timeout, MaxOutputBytes: spec.MaxOutputBytes}
	}
	return catalog, nil
}

var builtinCatalog = func() *RecipeCatalog {
	catalog, err := NewRecipeCatalog([]GoTestRecipeSpec{
		{ID: "go-test-workload", Version: "workload-v1", PackagePath: "./internal/workload", BuildTags: "repair_regression",
			ExpectedTestName: "TestRepairFixtureAcceptsSchemaTwo", ExpectedFailure: "repair fixture: unsupported job schema version 2", Timeout: 2 * time.Minute, MaxOutputBytes: 32 * 1024},
		{ID: "go-test-positive-int", Version: "positive-int-v1", PackagePath: "./testdata/code-repair/positive-int",
			ExpectedTestName: "TestParsePositiveAcceptsOne", ExpectedFailure: "positive-int fixture: one rejected", Timeout: 2 * time.Minute, MaxOutputBytes: 32 * 1024},
	})
	if err != nil {
		panic(err)
	}
	return catalog
}()

// ResolveTestRecipe accepts an opaque ID, never executable or argument text
// supplied by a model or incident. The actual process runs in repair-runner.
func (c *RecipeCatalog) Resolve(binding coderepair.RepositoryBinding, id string) (TestRecipe, error) {
	if err := binding.Validate(); err != nil {
		return TestRecipe{}, err
	}
	if !binding.Enabled {
		return TestRecipe{}, errors.New("repository binding is disabled")
	}
	allowed := false
	for _, configured := range binding.TestRecipes {
		if configured == id {
			allowed = true
			break
		}
	}
	if !allowed {
		return TestRecipe{}, errors.New("test recipe is not allowed by repository binding")
	}
	recipe, exists := c.recipes[id]
	if !exists {
		return TestRecipe{}, errors.New("test recipe ID is not registered")
	}
	recipe.Arguments = append([]string(nil), recipe.Arguments...)
	return recipe, nil
}

// ResolveTestRecipe accepts an opaque ID from an approved repository binding.
// Executable, arguments and bounds come from the trusted sandbox image catalog.
func ResolveTestRecipe(binding coderepair.RepositoryBinding, id string) (TestRecipe, error) {
	if binding.ValidationProfile != nil {
		if err := binding.Validate(); err != nil {
			return TestRecipe{}, err
		}
		p := binding.ValidationProfile
		if !binding.Enabled || id != p.ID {
			return TestRecipe{}, errors.New("validation profile is not enabled for this recipe")
		}
		return TestRecipe{ID: p.ID, Version: p.Version, Executable: p.Test.Executable, Arguments: append([]string{}, p.Test.Arguments...),
			ExpectedTestName: p.ExpectedTestName, ExpectedFailure: p.ExpectedFailure, Timeout: time.Duration(p.Test.TimeoutSeconds) * time.Second,
			MaxOutputBytes: 64 * 1024, Checks: p.Checks, RootFiles: p.RootFiles, RequireEmptyOutput: p.Test.RequireEmptyOutput}, nil
	}
	return builtinCatalog.Resolve(binding, id)
}
