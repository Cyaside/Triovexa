package agent

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/domain"
)

func testSnapshot(t *testing.T) coderepair.EvidenceSnapshot {
	t.Helper()
	now := time.Now().UTC()
	incident := domain.Incident{ID: "incident-1", ServiceName: "worker", Environment: "staging"}
	items := []domain.EvidenceItem{
		{ID: "metric-1", IncidentID: incident.ID, Type: "metric", Source: "workload-control",
			Snippet: "backlog rising", Timestamp: now,
			MetadataJSON: `{"target":"worker","complete":true,"deployed_revision":"` + strings.Repeat("a", 40) + `"}`},
		{ID: "log-1", IncidentID: incident.ID, Type: "log", Source: "grafana-loki",
			Snippet: "failed to parse schema", Timestamp: now},
	}
	snapshot, err := coderepair.BuildEvidenceSnapshot(incident, items, now,
		coderepair.EvidenceLimits{MaxItems: 10, MaxSnippetBytes: 1024, MaxTotalBytes: 2048, MaxAge: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestParseDecisionAcceptsOnlyTypedOperations(t *testing.T) {
	snapshot := testSnapshot(t)
	for name, value := range map[string]string{
		"list":    `{"operation":"list_files","prefix":"internal/workload"}`,
		"read":    `{"operation":"read_file","path":"internal/workload/worker.go"}`,
		"search":  `{"operation":"search_text","prefix":"internal/workload","query":"schema"}`,
		"patch":   `{"operation":"propose_patch","patch":"diff --git a/x b/x","hypothesis":"schema two fails","evidence_ids":["log-1"]}`,
		"test":    `{"operation":"run_allowed_test","recipe_id":"go-test-workload"}`,
		"unknown": `{"operation":"cannot_determine","reason":"no source-level cause found"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseDecision(value, snapshot); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestParseDecisionRejectsUnboundedAndUngroundedOutput(t *testing.T) {
	snapshot := testSnapshot(t)
	for name, value := range map[string]string{
		"shell operation":    `{"operation":"run_shell","command":"rm -rf /"}`,
		"unknown field":      `{"operation":"read_file","path":"internal/workload/worker.go","command":"cat /etc/passwd"}`,
		"mixed fields":       `{"operation":"read_file","path":"internal/workload/worker.go","recipe_id":"go-test-workload"}`,
		"untrusted citation": `{"operation":"propose_patch","patch":"diff","hypothesis":"bug","evidence_ids":["forged"]}`,
		"missing citation":   `{"operation":"propose_patch","patch":"diff","hypothesis":"bug"}`,
		"duplicate citation": `{"operation":"propose_patch","patch":"diff","hypothesis":"bug","evidence_ids":["log-1","log-1"]}`,
		"trailing object":    `{"operation":"cannot_determine","reason":"unknown"} {"operation":"read_file","path":"x"}`,
		"markdown wrapper":   "```json\n{\"operation\":\"cannot_determine\",\"reason\":\"unknown\"}\n```",
		"unsupported extras": `{"operation":"cannot_determine","reason":"unknown","patch":"diff"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseDecision(value, snapshot); err == nil {
				t.Fatal("invalid agent response was accepted")
			}
		})
	}
	snapshot.Entries[0].Status = coderepair.EvidenceStale
	encoded, err := json.Marshal(Decision{Operation: ProposePatch, Patch: "diff", Hypothesis: "bug", EvidenceIDs: []string{"metric-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseDecision(string(encoded), snapshot); err == nil {
		t.Fatal("mutated evidence snapshot was accepted")
	}
}
