package sandbox

import "testing"

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
