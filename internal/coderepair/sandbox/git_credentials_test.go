package sandbox

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrivateCheckoutCredentialIsEphemeralAndRedacted(t *testing.T) {
	const token = "github-fixture-private-token"
	t.Setenv("REPAIR_TEST_READ_TOKEN", token)
	b := testBinding()
	b.CredentialRef = "env:REPAIR_TEST_READ_TOKEN"
	ctx, err := gitCredentialContext(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	env := strings.Join(gitCredentialEnvironment(ctx), "\n")
	if !strings.Contains(env, "http.https://github.com/.extraheader") || !strings.Contains(env, encoded) {
		t.Fatal("credential was not confined to Git environment configuration")
	}
	if got := redactGitCredential(ctx, token+" "+encoded); strings.Contains(got, token) || strings.Contains(got, encoded) {
		t.Fatal("Git error leaked a credential")
	}
	root := t.TempDir()
	runTestGit(t, root, "init", "-b", "main")
	output, err := gitInput(ctx, nil, "-C", root, "config", "--get", "http.https://github.com/.extraheader")
	if err != nil || !strings.Contains(output, "[REDACTED]") || strings.Contains(output, token) || strings.Contains(output, encoded) {
		t.Fatalf("Git credential was not applied and redacted: %v", err)
	}
	if config, err := os.ReadFile(filepath.Join(root, ".git", "config")); err != nil || strings.Contains(string(config), "extraheader") {
		t.Fatal("checkout persisted credential configuration")
	}
	if gitCredentialEnvironment(context.Background()) != nil {
		t.Fatal("credential escaped its checkout context")
	}
	b.CredentialRef = "env:REPAIR_TEST_MISSING_TOKEN"
	t.Setenv("REPAIR_TEST_MISSING_TOKEN", "")
	if _, err = gitCredentialContext(context.Background(), b); err == nil {
		t.Fatal("missing private credential accepted")
	}
	file := filepath.Join(t.TempDir(), "read-token")
	if err = os.WriteFile(file, []byte(token+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	b.CredentialRef = "file:" + file
	if _, err = gitCredentialContext(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(file, []byte(strings.Repeat("x", 4097)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = gitCredentialContext(context.Background(), b); err == nil {
		t.Fatal("oversized private credential accepted")
	}
}
