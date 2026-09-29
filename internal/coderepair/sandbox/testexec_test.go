package sandbox

import (
	"context"
	"strings"
	"testing"
)

func TestRunAllowedTestRejectsCommandSelectionBeforeExecution(t *testing.T) {
	for _, id := range []string{"../../bin/sh", "go-test-workload; curl evil.invalid", "custom-shell"} {
		if _, err := RunAllowedTest(context.Background(), t.TempDir(), testBinding(), id); err == nil {
			t.Fatalf("executed unregistered recipe %q", id)
		}
	}
}

func TestRecipeEnvironmentOmitsProviderCredentials(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "secret-provider-key")
	t.Setenv("DATABASE_URL", "postgres://user:password@host/db")
	t.Setenv("GITHUB_TOKEN", "secret-github-token")
	t.Setenv("GOPATH", "/workspace/untrusted")
	t.Setenv("GOMODCACHE", "/workspace/untrusted-modules")
	env := strings.Join(recipeEnvironment(), "\n")
	for _, secret := range []string{"secret-provider-key", "secret-github-token", "password@host", "OPENAI_API_KEY", "DATABASE_URL", "GITHUB_TOKEN"} {
		if strings.Contains(env, secret) {
			t.Fatalf("test environment leaked %q", secret)
		}
	}
	for _, policy := range []string{"GOPATH=/go", "GOMODCACHE=/go/pkg/mod", "TMPDIR=/tmp", "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "GOWORK=off", "GOENV=off", "CGO_ENABLED=0", "GOMAXPROCS=2", "GOFLAGS=-mod=readonly -p=1"} {
		if !strings.Contains(env, policy) {
			t.Fatalf("test environment lacks %q", policy)
		}
	}
	if strings.Contains(env, "/workspace/untrusted") {
		t.Fatal("test environment inherited untrusted Go paths")
	}
}
