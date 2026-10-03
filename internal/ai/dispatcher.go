package ai

import (
	"context"
	"fmt"
	"time"

	"github.com/Cyaside/Triovexa/internal/security"
)

// ModelDispatcher owns admission and the external request. The serialized body
// includes the final messages; no client-side retry follows a dispatch error.
type ModelDispatcher interface {
	Dispatch(context.Context, ProviderConfig, []byte) (body []byte, status int, latency time.Duration, err error)
}

type CompletionCallError struct {
	Provider string
	Cause    error
}

func (e *CompletionCallError) Error() string {
	return fmt.Sprintf("call %s provider: %s", e.Provider, security.Redact(e.Cause.Error()))
}
func (e *CompletionCallError) Unwrap() error { return e.Cause }
