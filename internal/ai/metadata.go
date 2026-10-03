package ai

import (
	"fmt"
	"github.com/Cyaside/Triovexa/internal/security"
)

func CompletionMetadata(result CompletionResult) string {
	status := result.UsageStatus
	if status == "" {
		status = UsageMissing
	}
	return security.Redact(fmt.Sprintf("provider=%s model=%s latency_ms=%d prompt_tokens=%d completion_tokens=%d usage_status=%s", result.Provider, result.Model, result.Latency.Milliseconds(), result.Usage.PromptTokens, result.Usage.CompletionTokens, status))
}
