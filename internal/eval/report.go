package eval

import (
	"time"

	"github.com/Cyaside/Triovexa/internal/ai"
)

type Case struct {
	ID              string     `json:"id"`
	Title           string     `json:"title"`
	Service         string     `json:"service"`
	Environment     string     `json:"environment"`
	Severity        string     `json:"severity"`
	Evidence        []Evidence `json:"evidence"`
	Documents       []Document `json:"documents"`
	ExpectedAction  string     `json:"expected_action"`
	ExpectedActions []string   `json:"expected_actions,omitempty"`
}

type Evidence struct {
	Type     string         `json:"type"`
	Source   string         `json:"source"`
	Snippet  string         `json:"snippet"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type Document struct {
	Title     string `json:"title"`
	Type      string `json:"type"`
	Relevance string `json:"relevance"`
	Snippet   string `json:"snippet,omitempty"`
}

type UsageObservation struct {
	Phase    string             `json:"phase"`
	Provider string             `json:"provider,omitempty"`
	Model    string             `json:"model,omitempty"`
	Status   ai.UsageStatus     `json:"status"`
	Usage    ai.CompletionUsage `json:"reported_usage"`
}

type CaseResult struct {
	CaseID                   string             `json:"case_id"`
	Repeat                   int                `json:"repeat"`
	ExpectedActions          []string           `json:"expected_actions"`
	ProposedActions          []string           `json:"proposed_actions"`
	Accurate                 bool               `json:"accurate"`
	ExpectedActionsPresent   bool               `json:"expected_actions_present"`
	ExtraActions             int                `json:"extra_actions"`
	CitationValidityAssessed bool               `json:"citation_validity_assessed"`
	CitationsValid           bool               `json:"citations_valid"`
	GroundingStatus          string             `json:"grounding_status"`
	InvalidProposals         int                `json:"invalid_proposals"`
	FailureStage             string             `json:"failure_stage,omitempty"`
	FailureReason            string             `json:"failure_reason,omitempty"`
	LatencyMilliseconds      int64              `json:"latency_ms"`
	UsageObservations        []UsageObservation `json:"usage_observations,omitempty"`
}

type Summary struct {
	Runs                    int                `json:"runs"`
	Cases                   int                `json:"cases"`
	SuccessfulRuns          int                `json:"successful_runs"`
	FailedRuns              int                `json:"failed_runs"`
	AccuracyPercent         float64            `json:"accuracy_percent"`
	CitationValidityRuns    int                `json:"citation_validity_runs"`
	CitationValidityPercent float64            `json:"citation_validity_percent"`
	InvalidProposals        int                `json:"invalid_proposals"`
	ExtraActions            int                `json:"extra_actions"`
	AverageLatencyMS        float64            `json:"average_latency_ms"`
	UsageKnownCalls         int                `json:"usage_known_calls"`
	UsageMissingCalls       int                `json:"usage_missing_calls"`
	UsageInvalidCalls       int                `json:"usage_invalid_calls"`
	KnownUsageTokens        ai.CompletionUsage `json:"known_usage_tokens"`
}

type Report struct {
	SchemaVersion int          `json:"schema_version"`
	GeneratedAt   time.Time    `json:"generated_at"`
	Mode          string       `json:"mode"`
	Provider      string       `json:"provider,omitempty"`
	Model         string       `json:"model,omitempty"`
	PromptVersion string       `json:"prompt_version"`
	Summary       Summary      `json:"summary"`
	Results       []CaseResult `json:"results"`
}
