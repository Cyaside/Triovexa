package agent

import (
	"context"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/coderepair/sandbox"
)

// InvestigationEngine supplies proposals; the handler and domain store remain
// responsible for approval, fenced state changes and publication authority.
// A RecoveryOnly claim permits reconciliation of a completed durable proof;
// implementations must not dispatch new model requests, tools or tests.
type InvestigationEngine interface {
	Investigate(context.Context, *sandbox.Workspace, coderepair.RepositoryBinding,
		coderepair.EvidenceSnapshot, coderepair.AgentSelection, string) InvestigationResult
}

type ClaimedInvestigation struct {
	Job             coderepair.Job
	ExpectedVersion int64
	Case            coderepair.Case
	Attempt         coderepair.Attempt
	RecoveryOnly    bool
}

type investigationContextKey struct{}

func WithClaimedInvestigation(ctx context.Context, claim ClaimedInvestigation) context.Context {
	return context.WithValue(ctx, investigationContextKey{}, claim)
}

func InvestigationClaim(ctx context.Context) (ClaimedInvestigation, bool) {
	claim, ok := ctx.Value(investigationContextKey{}).(ClaimedInvestigation)
	return claim, ok
}
