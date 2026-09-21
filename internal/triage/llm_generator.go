package triage

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Cyaside/Triovexa/internal/ai"
	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/mode"
)

type LLMGenerator struct {
	client ai.JSONCompleter
	now    func() time.Time
}

func NewLLMGenerator(client ai.JSONCompleter) *LLMGenerator {
	return &LLMGenerator{
		client: client,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
}

func (g *LLMGenerator) Generate(
	ctx context.Context,
	incident domain.Incident,
	evidence []domain.EvidenceItem,
	documents []domain.DocumentReference,
) (domain.TriageResult, error) {
	if g.client == nil {
		return domain.TriageResult{}, fmt.Errorf("llm triage client is not configured")
	}

	systemPrompt := "You are an incident triage operator. Return ONLY valid JSON in English without Markdown."
	userPrompt := buildTriagePrompt(incident, evidence, documents)

	messages := []ai.ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}
	var content string
	var providerMetadata string
	var err error
	if detailed, ok := g.client.(ai.DetailedJSONCompleter); ok {
		completion, completionErr := detailed.CompleteJSONDetailed(ctx, messages)
		err = completionErr
		content = completion.Content
		if completionErr == nil {
			providerMetadata = fmt.Sprintf("provider=%s model=%s latency_ms=%d prompt_tokens=%d completion_tokens=%d", completion.Provider, completion.Model, completion.Latency.Milliseconds(), completion.Usage.PromptTokens, completion.Usage.CompletionTokens)
		}
	} else {
		content, err = g.client.CompleteJSON(ctx, messages)
	}
	if err != nil {
		return domain.TriageResult{}, err
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		return domain.TriageResult{}, fmt.Errorf("decode llm triage payload: %w", err)
	}

	result := domain.TriageResult{
		ID:                uuid.NewString(),
		IncidentID:        incident.ID,
		Summary:           coerceString(payload["summary"]),
		Hypotheses:        compactList(coerceStringList(payload["hypotheses"]), 4),
		BlastRadius:       coerceString(payload["blast_radius"]),
		NextSteps:         compactList(coerceStringList(payload["next_steps"]), 5),
		DraftStatusUpdate: coerceString(payload["draft_status_update"]),
		ConfidenceNotes:   coerceString(payload["confidence_notes"]),
		CreatedAt:         g.now(),
	}

	if result.Summary == "" {
		return domain.TriageResult{}, fmt.Errorf("llm triage payload did not include summary")
	}
	if result.DraftStatusUpdate == "" {
		result.DraftStatusUpdate = fmt.Sprintf("[%s] %s", strings.ToUpper(incident.Severity), result.Summary)
	}
	if result.BlastRadius == "" {
		result.BlastRadius = "the blast radius is not clear enough and requires operator verification"
	}
	if result.ConfidenceNotes == "" {
		result.ConfidenceNotes = "the model did not provide confidence notes; use evidence and documents for manual validation"
	}
	if providerMetadata != "" {
		result.ConfidenceNotes = strings.TrimSpace(result.ConfidenceNotes + "; " + providerMetadata + "; prompt_version=triage-v1")
	}

	return result, nil
}

type SwitchingGenerator struct {
	modes     *mode.Manager
	heuristic interface {
		Generate(context.Context, domain.Incident, []domain.EvidenceItem, []domain.DocumentReference) (domain.TriageResult, error)
	}
	llm interface {
		Generate(context.Context, domain.Incident, []domain.EvidenceItem, []domain.DocumentReference) (domain.TriageResult, error)
	}
}

func NewSwitchingGenerator(
	modes *mode.Manager,
	heuristic interface {
		Generate(context.Context, domain.Incident, []domain.EvidenceItem, []domain.DocumentReference) (domain.TriageResult, error)
	},
	llm interface {
		Generate(context.Context, domain.Incident, []domain.EvidenceItem, []domain.DocumentReference) (domain.TriageResult, error)
	},
) *SwitchingGenerator {
	return &SwitchingGenerator{
		modes:     modes,
		heuristic: heuristic,
		llm:       llm,
	}
}

func (g *SwitchingGenerator) Generate(
	ctx context.Context,
	incident domain.Incident,
	evidence []domain.EvidenceItem,
	documents []domain.DocumentReference,
) (domain.TriageResult, error) {
	if g.modes != nil && isLLMReasoning(g.modes.Snapshot().Reasoning) && g.llm != nil {
		result, err := g.llm.Generate(ctx, incident, evidence, documents)
		if err == nil {
			return result, nil
		}
		fallback, fallbackErr := g.heuristic.Generate(ctx, incident, evidence, documents)
		if fallbackErr != nil {
			return domain.TriageResult{}, fallbackErr
		}
		fallback.ConfidenceNotes = strings.TrimSpace(fallback.ConfidenceNotes + "; llm_fallback=true; fallback_reason=" + classifyFallback(err))
		return fallback, nil
	}

	return g.heuristic.Generate(ctx, incident, evidence, documents)
}

func classifyFallback(err error) string {
	if err == nil {
		return "unknown"
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "timeout"), strings.Contains(message, "deadline"):
		return "timeout"
	case strings.Contains(message, "json"), strings.Contains(message, "decode"):
		return "invalid_response"
	case strings.Contains(message, "configured"), strings.Contains(message, "credential"):
		return "not_configured"
	default:
		return "provider_error"
	}
}

func isLLMReasoning(reasoning mode.Reasoning) bool {
	return reasoning == mode.ReasoningLLM
}

func buildTriagePrompt(
	incident domain.Incident,
	evidence []domain.EvidenceItem,
	documents []domain.DocumentReference,
) string {
	var builder strings.Builder
	builder.WriteString("Summarize the following incident as JSON using this schema:\n")
	builder.WriteString(`{"summary":"","hypotheses":[],"blast_radius":"","next_steps":[],"draft_status_update":"","confidence_notes":""}`)
	builder.WriteString("\n\nIncident:\n")
	builder.WriteString(fmt.Sprintf("- title: %s\n- service: %s\n- environment: %s\n- severity: %s\n", incident.Title, incident.ServiceName, incident.Environment, incident.Severity))
	builder.WriteString("\nEvidence:\n")
	builder.WriteString(buildEvidenceDigest(evidence))
	builder.WriteString("\n\nRelevant documents:\n")
	builder.WriteString(buildDocumentDigest(documents))
	builder.WriteString("\n\nRules:\n- Use at most 4 hypotheses.\n- Use at most 5 next_steps.\n- Prioritize actionable incident-response context.\n- Write every user-facing value in English.\n- Do not output Markdown.")
	return builder.String()
}

func buildEvidenceDigest(evidence []domain.EvidenceItem) string {
	if len(evidence) == 0 {
		return "- no evidence"
	}

	var lines []string
	for _, item := range evidence {
		lines = append(lines, fmt.Sprintf("- [%s] %s: %s", item.Type, item.Source, strings.TrimSpace(item.Snippet)))
		if len(lines) >= 8 {
			break
		}
	}
	return strings.Join(lines, "\n")
}

func buildDocumentDigest(documents []domain.DocumentReference) string {
	if len(documents) == 0 {
		return "- no relevant documents"
	}

	var lines []string
	for _, doc := range documents {
		lines = append(lines, fmt.Sprintf("- [%s] %s: %s", doc.DocumentType, doc.DocumentTitle, strings.TrimSpace(doc.RelevanceReason)))
		if len(lines) >= 5 {
			break
		}
	}
	return strings.Join(lines, "\n")
}

func compactList(values []string, limit int) []string {
	seen := make(map[string]struct{})
	filtered := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		filtered = append(filtered, value)
		if limit > 0 && len(filtered) >= limit {
			break
		}
	}
	return filtered
}

func coerceString(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case float64:
		return strings.TrimSpace(fmt.Sprintf("%.2f", typed))
	case map[string]any:
		for _, key := range []string{"text", "summary", "value", "title", "content"} {
			if nested, ok := typed[key]; ok {
				if coerced := coerceString(nested); coerced != "" {
					return coerced
				}
			}
		}
	}

	return ""
}

func coerceStringList(value any) []string {
	rawItems, ok := value.([]any)
	if !ok {
		if typed, ok := value.([]string); ok {
			return typed
		}
		return nil
	}

	items := make([]string, 0, len(rawItems))
	for _, item := range rawItems {
		if coerced := coerceString(item); coerced != "" {
			items = append(items, coerced)
		}
	}
	return items
}
