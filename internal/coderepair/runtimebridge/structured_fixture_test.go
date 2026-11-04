package runtimebridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os/exec"
	"strings"
	"testing"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/coderepair/agent"
	"github.com/Cyaside/Triovexa/internal/coderepair/sandbox"
)

type investigationFunc func(context.Context, *sandbox.Workspace, coderepair.RepositoryBinding, coderepair.EvidenceSnapshot, coderepair.AgentSelection, string) agent.InvestigationResult

func (f investigationFunc) Investigate(ctx context.Context, w *sandbox.Workspace, b coderepair.RepositoryBinding, s coderepair.EvidenceSnapshot, selection coderepair.AgentSelection, recipe string) agent.InvestigationResult {
	return f(ctx, w, b, s, selection, recipe)
}

// Translate existing deterministic test diffs into the new model contract.
// This helper is test-only; production never accepts or repairs a raw diff.
func structuredArgs(t *testing.T, root, patch, hypothesis string, ids []string) map[string]any {
	t.Helper()
	var files []sandbox.FileEdits
	var current *sandbox.FileEdits
	var old, next strings.Builder
	flush := func() {
		if current != nil && old.String() != next.String() {
			current.Edits = append(current.Edits, sandbox.TextEdit{OldText: old.String(), NewText: next.String()})
		}
		old.Reset()
		next.Reset()
	}
	for _, line := range strings.Split(strings.TrimRight(patch, "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flush()
			current = nil
		case strings.HasPrefix(line, "--- a/"):
			name := strings.TrimPrefix(line, "--- a/")
			blob, err := exec.Command("git", "-C", root, "show", "HEAD:"+name).Output()
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(blob)
			files = append(files, sandbox.FileEdits{Path: name, ExpectedDigest: hex.EncodeToString(sum[:])})
			current = &files[len(files)-1]
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "index "):
		case strings.HasPrefix(line, "@@"):
			flush()
		case strings.HasPrefix(line, "-"):
			old.WriteString(strings.TrimPrefix(line, "-") + "\n")
		case strings.HasPrefix(line, "+"):
			next.WriteString(strings.TrimPrefix(line, "+") + "\n")
		case strings.HasPrefix(line, " "):
			old.WriteString(line[1:] + "\n")
			next.WriteString(line[1:] + "\n")
		}
	}
	flush()
	return map[string]any{"format": sandbox.EditFormat, "files": files, "hypothesis": hypothesis, "evidence_ids": ids}
}
