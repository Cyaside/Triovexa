package coderepair

import "testing"

func profileFixture() *ValidationProfile {
	return &ValidationProfile{ID: "python-tests", Version: "python-v1", Image: "triovexa-repair-sandbox:python",
		RootFiles: []string{"pyproject.toml"}, ProtectedPaths: []string{"tests"},
		Test:             ValidationCommand{Executable: "/usr/local/bin/python", Arguments: []string{"-m", "unittest", "discover", "-s", "tests", "-v"}, TimeoutSeconds: 60},
		ExpectedTestName: "test_accepts_one", ExpectedFailure: "one rejected"}
}

func TestValidationProfilePinsLanguageNeutralScope(t *testing.T) {
	b := RepositoryBinding{ID: "binding", ServiceName: "worker", Environment: "staging", RepositoryURL: "https://github.com/owner/repo", BaseRef: "main", PolicyVersion: "repair-v2", Enabled: true}
	b.AllowedPaths, b.TestRecipes = []string{"src", "tests", "pyproject.toml"}, []string{"python-tests"}
	b.ValidationProfile = profileFixture()
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"src/parser.py", "src/parser.ts", "src/parser.rs"} {
		if !b.CanPatchPath(name) {
			t.Fatalf("source rejected: %s", name)
		}
	}
	for _, name := range []string{"tests/test_parser.py", "pyproject.toml", "src/../../.env", "other/parser.py"} {
		if b.CanPatchPath(name) {
			t.Fatalf("protected path accepted: %s", name)
		}
	}
	for _, mutate := range []func(*ValidationProfile){
		func(p *ValidationProfile) { p.Test.Executable = "/bin/sh" },
		func(p *ValidationProfile) { p.Test.Arguments = []string{"-c", "print('bypass')"} },
		func(p *ValidationProfile) { p.RootFiles = []string{"../pyproject.toml"} },
		func(p *ValidationProfile) { p.ProtectedPaths = nil },
		func(p *ValidationProfile) { p.Test.TimeoutSeconds = 301 },
	} {
		p := profileFixture()
		mutate(p)
		if p.Validate() == nil {
			t.Fatalf("unsafe profile accepted: %+v", p)
		}
	}
}
