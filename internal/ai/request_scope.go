package ai

import "context"

// RequestScope is supplied by trusted workflow code. An SDK, tool payload or
// retry cannot create a new campaign; all scopes use the configured ledger.
type RequestScope struct {
	RunID   string
	Phase   string
	Ordinal int
}

type requestScopeKey struct{}

func WithRequestScope(ctx context.Context, scope RequestScope) context.Context {
	return context.WithValue(ctx, requestScopeKey{}, scope)
}

func RequestScopeFrom(ctx context.Context) (RequestScope, bool) {
	scope, ok := ctx.Value(requestScopeKey{}).(RequestScope)
	return scope, ok && scope.RunID != "" && scope.Phase != "" && scope.Ordinal > 0
}
