package sandbox

import (
	"errors"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
)

type TestRecipe struct {
	ID             string
	Executable     string
	Arguments      []string
	Timeout        time.Duration
	MaxOutputBytes int
}

var builtinTestRecipes = map[string]TestRecipe{
	"go-test-workload": {
		ID: "go-test-workload", Executable: "/usr/local/go/bin/go",
		Arguments: []string{"test", "-tags", "repair_regression", "./internal/workload", "-run", "^TestRepairFixtureAcceptsSchemaTwo$", "-count=1"},
		Timeout:   2 * time.Minute, MaxOutputBytes: 32 * 1024,
	},
}

// ResolveTestRecipe accepts an opaque ID, never executable or argument text
// supplied by a model or incident. The actual process runs in repair-runner.
func ResolveTestRecipe(binding coderepair.RepositoryBinding, id string) (TestRecipe, error) {
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
	recipe, exists := builtinTestRecipes[id]
	if !exists {
		return TestRecipe{}, errors.New("test recipe ID is not registered")
	}
	recipe.Arguments = append([]string(nil), recipe.Arguments...)
	return recipe, nil
}
