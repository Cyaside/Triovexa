package agent

import (
	"context"

	"github.com/Cyaside/Triovexa/internal/ai"
	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/coderepair/sandbox"
)

// TestRunner executes a registered recipe inside the approved sandbox.
type TestRunner interface {
	Run(context.Context, string, coderepair.RepositoryBinding, string) (sandbox.TestResult, error)
}

type InvestigationResult struct {
	Status      coderepair.State
	Code        string
	Reason      string
	Hypothesis  string
	EvidenceIDs []string
	Patch       string
	PatchReport sandbox.PatchReport
	Before      sandbox.TestResult
	After       sandbox.TestResult
	Steps       int
	Usage       ai.CompletionUsage
	Provider    string
	Model       string
	Prompt      string
	Runtime     *RuntimeTrace
}
