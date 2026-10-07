package sandbox

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/Cyaside/Triovexa/internal/coderepair"
)

type gitCredentialKey struct{}

// Resolve references only in the trusted checkout process, never in the agent
// or sandbox. Git receives the header in its environment, not URLs/arguments.
func gitCredentialContext(ctx context.Context, binding coderepair.RepositoryBinding) (context.Context, error) {
	if err := coderepair.ValidateCredentialReference(binding.CredentialRef); err != nil {
		return nil, err
	}
	if binding.CredentialRef == "" {
		return ctx, nil
	}
	var token string
	if strings.HasPrefix(binding.CredentialRef, "env:") {
		token = os.Getenv(strings.TrimPrefix(binding.CredentialRef, "env:"))
	} else {
		name := strings.TrimPrefix(binding.CredentialRef, "file:")
		info, err := os.Lstat(name)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 4096 {
			return nil, errors.New("GitHub credential file is missing or unsafe")
		}
		file, err := os.Open(name)
		if err != nil {
			return nil, errors.New("GitHub credential file is unavailable")
		}
		data, err := io.ReadAll(io.LimitReader(file, 4097))
		_ = file.Close()
		if err != nil || len(data) > 4096 {
			return nil, errors.New("GitHub credential cannot be read")
		}
		token = string(data)
	}
	token = strings.TrimSpace(token)
	if len(token) < 8 || len(token) > 4096 || strings.ContainsAny(token, " \t\r\n\x00") {
		return nil, errors.New("GitHub credential reference is not configured")
	}
	return context.WithValue(ctx, gitCredentialKey{}, token), nil
}

func gitCredentialEnvironment(ctx context.Context) []string {
	token, _ := ctx.Value(gitCredentialKey{}).(string)
	if token == "" {
		return nil
	}
	return []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.https://github.com/.extraheader",
		"GIT_CONFIG_VALUE_0=AUTHORIZATION: basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token))}
}

func redactGitCredential(ctx context.Context, output string) string {
	token, _ := ctx.Value(gitCredentialKey{}).(string)
	if token != "" {
		output = strings.ReplaceAll(output, token, "[REDACTED]")
		output = strings.ReplaceAll(output, base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token)), "[REDACTED]")
	}
	return output
}
