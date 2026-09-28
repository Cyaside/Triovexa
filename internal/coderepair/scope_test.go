package coderepair

import "testing"

func TestRepositoryBindingRejectsUnsafeInputs(t *testing.T) {
	base := RepositoryBinding{
		ID: "binding-1", ServiceName: "queue-worker", Environment: "staging",
		RepositoryURL: "https://github.com/Cyaside/Triovexa", BaseRef: "main",
		AllowedPaths: []string{"internal/workload"}, TestRecipes: []string{"go-test-workload"}, PolicyVersion: "repair-v1",
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid binding: %v", err)
	}
	for _, rawURL := range []string{
		"http://github.com/Cyaside/Triovexa",
		"https://github.com.evil.test/Cyaside/Triovexa",
		"https://user:password@github.com/Cyaside/Triovexa",
		"https://github.com/Cyaside/Triovexa?token=secret",
		"https://github.com/Cyaside/Triovexa/../other",
		"https://github.com/Cyaside/Triovexa/extra",
	} {
		binding := base
		binding.RepositoryURL = rawURL
		if err := binding.Validate(); err == nil {
			t.Errorf("accepted unsafe repository URL %q", rawURL)
		}
	}
	for _, name := range []string{
		".", "../workload", "internal/../.github", "/etc/passwd", "C:/secrets",
		`internal\workload`, "internal//workload", ".github/workflows", ".env.local", "secrets/key.pem",
	} {
		binding := base
		binding.AllowedPaths = []string{name}
		if err := binding.Validate(); err == nil {
			t.Errorf("accepted unsafe allowed path %q", name)
		}
	}
	binding := base
	binding.TestRecipes = []string{"go test ./... && curl evil.test"}
	if err := binding.Validate(); err == nil {
		t.Fatal("accepted a command as a test recipe ID")
	}
	for _, ref := range []string{"--upload-pack=sh", "main..evil", "feature/.hidden", "feature/", "main.lock"} {
		binding := base
		binding.BaseRef = ref
		if err := binding.Validate(); err == nil {
			t.Errorf("accepted unsafe base ref %q", ref)
		}
	}
}

func TestRepositoryBindingPathBoundary(t *testing.T) {
	binding := RepositoryBinding{AllowedPaths: []string{"internal/workload"}}
	for _, name := range []string{"internal/workload", "internal/workload/workload.go"} {
		if !binding.AllowsPath(name) {
			t.Errorf("expected %q to be allowed", name)
		}
	}
	for _, name := range []string{
		"internal/workload-other/main.go", "internal/workload/../../.env", "internal/workload/.git/config",
		"internal/workload/secret.key", "internal/workload\\repair_fixture.go",
	} {
		if binding.AllowsPath(name) {
			t.Errorf("expected %q to be denied", name)
		}
	}
}
