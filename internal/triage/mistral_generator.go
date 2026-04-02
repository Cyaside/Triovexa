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

type MistralGenerator struct {
	client ai.JSONCompleter
	now    func() time.Time
}

func NewMistralGenerator(client ai.JSONCompleter) *MistralGenerator {
	return &MistralGenerator{
		client: client,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
}

func (g *MistralGenerator) Generate(
	ctx context.Context,
	incident domain.Incident,
	evidence []domain.EvidenceItem,
	documents []domain.DocumentReference,
) (domain.TriageResult, error) {
	if g.client == nil {
		return domain.TriageResult{}, fmt.Errorf("mistral triage client is not configured")
	}

	systemPrompt := "Anda adalah AI incident triage operator. Kembalikan HANYA JSON valid dalam Bahasa Indonesia tanpa markdown."
	userPrompt := buildTriagePrompt(incident, evidence, documents)

	content, err := g.client.CompleteJSON(ctx, []ai.ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	})
	if err != nil {
		return domain.TriageResult{}, err
	}

	var payload struct {
		Summary           string   `json:"summary"`
		Hypotheses        []string `json:"hypotheses"`
		BlastRadius       string   `json:"blast_radius"`
		NextSteps         []string `json:"next_steps"`
		DraftStatusUpdate string   `json:"draft_status_update"`
		ConfidenceNotes   string   `json:"confidence_notes"`
	}
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		return domain.TriageResult{}, fmt.Errorf("decode mistral triage payload: %w", err)
	}

	result := domain.TriageResult{
		ID:                uuid.NewString(),
		IncidentID:        incident.ID,
		Summary:           strings.TrimSpace(payload.Summary),
		Hypotheses:        compactList(payload.Hypotheses, 4),
		BlastRadius:       strings.TrimSpace(payload.BlastRadius),
		NextSteps:         compactList(payload.NextSteps, 5),
		DraftStatusUpdate: strings.TrimSpace(payload.DraftStatusUpdate),
		ConfidenceNotes:   strings.TrimSpace(payload.ConfidenceNotes),
		CreatedAt:         g.now(),
	}

	if result.Summary == "" {
		return domain.TriageResult{}, fmt.Errorf("mistral triage payload did not include summary")
	}
	if result.DraftStatusUpdate == "" {
		result.DraftStatusUpdate = fmt.Sprintf("[%s] %s", strings.ToUpper(incident.Severity), result.Summary)
	}
	if result.BlastRadius == "" {
		result.BlastRadius = "blast radius belum cukup jelas; perlu verifikasi operator"
	}
	if result.ConfidenceNotes == "" {
		result.ConfidenceNotes = "confidence tidak diberikan model; gunakan evidence dan dokumen untuk validasi manual"
	}

	return result, nil
}

type SwitchingGenerator struct {
	modes     *mode.Manager
	heuristic interface {
		Generate(context.Context, domain.Incident, []domain.EvidenceItem, []domain.DocumentReference) (domain.TriageResult, error)
	}
	mistral interface {
		Generate(context.Context, domain.Incident, []domain.EvidenceItem, []domain.DocumentReference) (domain.TriageResult, error)
	}
}

func NewSwitchingGenerator(
	modes *mode.Manager,
	heuristic interface {
		Generate(context.Context, domain.Incident, []domain.EvidenceItem, []domain.DocumentReference) (domain.TriageResult, error)
	},
	mistral interface {
		Generate(context.Context, domain.Incident, []domain.EvidenceItem, []domain.DocumentReference) (domain.TriageResult, error)
	},
) *SwitchingGenerator {
	return &SwitchingGenerator{
		modes:     modes,
		heuristic: heuristic,
		mistral:   mistral,
	}
}

func (g *SwitchingGenerator) Generate(
	ctx context.Context,
	incident domain.Incident,
	evidence []domain.EvidenceItem,
	documents []domain.DocumentReference,
) (domain.TriageResult, error) {
	if g.modes != nil && g.modes.Snapshot().Reasoning == mode.ReasoningMistral && g.mistral != nil {
		return g.mistral.Generate(ctx, incident, evidence, documents)
	}

	return g.heuristic.Generate(ctx, incident, evidence, documents)
}

func buildTriagePrompt(
	incident domain.Incident,
	evidence []domain.EvidenceItem,
	documents []domain.DocumentReference,
) string {
	var builder strings.Builder
	builder.WriteString("Ringkas incident berikut ke JSON dengan schema:\n")
	builder.WriteString(`{"summary":"","hypotheses":[],"blast_radius":"","next_steps":[],"draft_status_update":"","confidence_notes":""}`)
	builder.WriteString("\n\nIncident:\n")
	builder.WriteString(fmt.Sprintf("- title: %s\n- service: %s\n- environment: %s\n- severity: %s\n", incident.Title, incident.ServiceName, incident.Environment, incident.Severity))
	builder.WriteString("\nEvidence:\n")
	builder.WriteString(buildEvidenceDigest(evidence))
	builder.WriteString("\n\nRelevant documents:\n")
	builder.WriteString(buildDocumentDigest(documents))
	builder.WriteString("\n\nAturan:\n- Maksimal 4 hypotheses.\n- Maksimal 5 next_steps.\n- Prioritaskan konteks incident response yang operasional.\n- Jangan keluarkan markdown.")
	return builder.String()
}

func buildEvidenceDigest(evidence []domain.EvidenceItem) string {
	if len(evidence) == 0 {
		return "- tidak ada evidence"
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
		return "- tidak ada dokumen relevan"
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
