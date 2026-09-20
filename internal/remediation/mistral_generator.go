package remediation

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Cyaside/Triovexa/internal/ai"
	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/execution"
	"github.com/Cyaside/Triovexa/internal/mode"
)

type MistralGenerator struct {
	catalog execution.Catalog
	client  ai.JSONCompleter
	now     func() time.Time
}

type LLMGenerator = MistralGenerator

func NewMistralGenerator(catalog execution.Catalog, client ai.JSONCompleter) *MistralGenerator {
	return &MistralGenerator{
		catalog: catalog,
		client:  client,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
}

func NewLLMGenerator(catalog execution.Catalog, client ai.JSONCompleter) *LLMGenerator {
	return NewMistralGenerator(catalog, client)
}

func (g *MistralGenerator) CatalogMode() string {
	return "llm-constrained"
}

func (g *MistralGenerator) Generate(
	ctx context.Context,
	incident domain.Incident,
	triage domain.TriageResult,
	evidence []domain.EvidenceItem,
	documents []domain.DocumentReference,
) ([]domain.CandidateAction, error) {
	if g.client == nil {
		return nil, fmt.Errorf("llm remediation client is not configured")
	}

	systemPrompt := "You select candidate incident-response actions. Choose ONLY actions from the supplied catalog. Return ONLY valid JSON in English without Markdown."
	userPrompt := buildActionPrompt(incident, triage, evidence, documents, g.catalog)

	content, err := g.client.CompleteJSON(ctx, []ai.ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	})
	if err != nil {
		return nil, err
	}

	var payload struct {
		Actions []struct {
			ActionType     string         `json:"action_type"`
			TargetResource string         `json:"target_resource"`
			Parameters     map[string]any `json:"parameters"`
			Rationale      string         `json:"rationale"`
			EvidenceRefs   []string       `json:"evidence_refs"`
		} `json:"actions"`
	}
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		return nil, fmt.Errorf("decode llm action payload: %w", err)
	}

	helper := &HeuristicGenerator{catalog: g.catalog}
	actions := make([]domain.CandidateAction, 0, len(payload.Actions))
	for _, draft := range payload.Actions {
		actionType := strings.TrimSpace(draft.ActionType)
		if actionType == "" {
			continue
		}

		if _, ok := g.catalog.Get(actionType); !ok {
			actions = append(actions, domain.CandidateAction{
				ID:             uuid.NewString(),
				IncidentID:     incident.ID,
				ActionType:     actionType,
				TargetResource: strings.TrimSpace(draft.TargetResource),
				Rationale:      strings.TrimSpace(draft.Rationale),
				EvidenceRefs:   compactStrings(draft.EvidenceRefs),
				ApprovalHint:   "validation failed: action is not present in the catalog",
				Status:         domain.CandidateActionStatusInvalid,
				CreatedAt:      g.now(),
			})
			continue
		}

		action, err := helper.newAction(
			incident,
			actionType,
			strings.TrimSpace(draft.TargetResource),
			draft.Parameters,
			compactStrings(draft.EvidenceRefs),
			strings.TrimSpace(draft.Rationale),
		)
		if err != nil {
			return nil, err
		}

		action = helper.validateAction(incident, action)
		actions = append(actions, action)
		if len(actions) >= 4 {
			break
		}
	}

	return actions, nil
}

type SwitchingGenerator struct {
	modes     *mode.Manager
	heuristic interface {
		Generate(context.Context, domain.Incident, domain.TriageResult, []domain.EvidenceItem, []domain.DocumentReference) ([]domain.CandidateAction, error)
	}
	llm interface {
		Generate(context.Context, domain.Incident, domain.TriageResult, []domain.EvidenceItem, []domain.DocumentReference) ([]domain.CandidateAction, error)
	}
}

func NewSwitchingGenerator(
	modes *mode.Manager,
	heuristic interface {
		Generate(context.Context, domain.Incident, domain.TriageResult, []domain.EvidenceItem, []domain.DocumentReference) ([]domain.CandidateAction, error)
	},
	llm interface {
		Generate(context.Context, domain.Incident, domain.TriageResult, []domain.EvidenceItem, []domain.DocumentReference) ([]domain.CandidateAction, error)
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
	triage domain.TriageResult,
	evidence []domain.EvidenceItem,
	documents []domain.DocumentReference,
) ([]domain.CandidateAction, error) {
	if g.modes != nil && isLLMReasoning(g.modes.Snapshot().Reasoning) && g.llm != nil {
		actions, err := g.llm.Generate(ctx, incident, triage, evidence, documents)
		if err == nil {
			return actions, nil
		}
	}

	return g.heuristic.Generate(ctx, incident, triage, evidence, documents)
}

func (g *SwitchingGenerator) CatalogMode() string {
	if g.modes != nil && g.modes.Snapshot().Reasoning == mode.ReasoningLLM {
		return "llm-constrained"
	}
	if g.modes != nil && g.modes.Snapshot().Reasoning == mode.ReasoningMistral {
		return "mistral-constrained"
	}

	return "heuristic-constrained"
}

func isLLMReasoning(reasoning mode.Reasoning) bool {
	return reasoning == mode.ReasoningLLM || reasoning == mode.ReasoningMistral
}

func buildActionPrompt(
	incident domain.Incident,
	triage domain.TriageResult,
	evidence []domain.EvidenceItem,
	documents []domain.DocumentReference,
	catalog execution.Catalog,
) string {
	var builder strings.Builder
	builder.WriteString("Select the safest candidate actions for the following incident.\n")
	builder.WriteString(`Return the JSON schema {"actions":[{"action_type":"","target_resource":"","parameters":{},"rationale":"","evidence_refs":[]}]} without additional fields.`)
	builder.WriteString("\n\nIncident:\n")
	builder.WriteString(fmt.Sprintf("- title: %s\n- service: %s\n- environment: %s\n- severity: %s\n", incident.Title, incident.ServiceName, incident.Environment, incident.Severity))
	builder.WriteString(fmt.Sprintf("- triage_summary: %s\n- blast_radius: %s\n", triage.Summary, triage.BlastRadius))
	builder.WriteString("\nEvidence:\n")
	builder.WriteString(buildEvidenceDigest(evidence))
	builder.WriteString("\n\nRelevant documents:\n")
	builder.WriteString(buildDocumentDigest(documents))
	builder.WriteString("\n\nCatalog:\n")
	builder.WriteString(buildCatalogDigest(catalog))
	builder.WriteString("\n\nRules:\n- Select at most 4 actions.\n- Use only action_type values from the catalog.\n- Use valid targets and reasonable parameters.\n- Prioritize low-risk actions.\n- Write rationales in English.\n- evidence_refs must contain relevant evidence IDs when available.")
	return builder.String()
}

func buildCatalogDigest(catalog execution.Catalog) string {
	keys := make([]string, 0, len(catalog))
	for key := range catalog {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var lines []string
	for _, key := range keys {
		item := catalog[key]
		lines = append(lines, fmt.Sprintf(
			"- key=%s risk=%s approval_required=%t executable=%t targets=%s params=%s description=%s",
			item.Key,
			item.RiskLevel,
			item.ApprovalRequired,
			item.Executable,
			strings.Join(item.AllowedTargets, "|"),
			describeParameters(item.Parameters),
			item.Description,
		))
	}
	return strings.Join(lines, "\n")
}

func describeParameters(parameters []execution.ParameterDefinition) string {
	if len(parameters) == 0 {
		return "none"
	}

	parts := make([]string, 0, len(parameters))
	for _, parameter := range parameters {
		required := "optional"
		if parameter.Required {
			required = "required"
		}
		parts = append(parts, fmt.Sprintf("%s(%s)", parameter.Name, required))
	}
	return strings.Join(parts, ", ")
}

func buildEvidenceDigest(evidence []domain.EvidenceItem) string {
	if len(evidence) == 0 {
		return "- no evidence"
	}

	lines := make([]string, 0, len(evidence))
	for _, item := range evidence {
		lines = append(lines, fmt.Sprintf("- [%s] %s %s: %s", item.ID, item.Type, item.Source, strings.TrimSpace(item.Snippet)))
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

	lines := make([]string, 0, len(documents))
	for _, doc := range documents {
		lines = append(lines, fmt.Sprintf("- [%s] %s: %s", doc.DocumentType, doc.DocumentTitle, strings.TrimSpace(doc.RelevanceReason)))
		if len(lines) >= 5 {
			break
		}
	}
	return strings.Join(lines, "\n")
}
