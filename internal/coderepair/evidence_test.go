package coderepair

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/domain"
)

type fixedEvidenceLister []domain.EvidenceItem

func (items fixedEvidenceLister) ListEvidenceItems(context.Context, string) ([]domain.EvidenceItem, error) {
	return items, nil
}

func evidenceFixture(now time.Time) (domain.Incident, RepositoryBinding, []domain.EvidenceItem) {
	incident := domain.Incident{ID: "incident-1", ServiceName: "queue-worker", Environment: "staging", State: domain.IncidentStateEscalated}
	binding := RepositoryBinding{
		ID: "binding-1", ServiceName: incident.ServiceName, Environment: incident.Environment,
		RepositoryURL: "https://github.com/Cyaside/Triovexa", BaseRef: "main",
		AllowedPaths: []string{"internal/workload"}, TestRecipes: []string{"go-test-workload"},
		PolicyVersion: "repair-v1", Enabled: true,
	}
	items := []domain.EvidenceItem{
		{ID: "metric-1", IncidentID: incident.ID, Type: "metric", Source: "workload-control",
			Snippet: "worker unhealthy, backlog=42", Timestamp: now,
			MetadataJSON: `{"target":"queue-worker","complete":true,"deployed_revision":"` + strings.Repeat("a", 40) + `","api_key":"raw-secret-metadata"}`},
		{ID: "log-1", IncidentID: incident.ID, Type: "log", Source: "grafana-loki",
			Snippet: "Authorization: Bearer abc123; owner alice@example.com; failed to decode schema", Timestamp: now},
	}
	return incident, binding, items
}

func TestEvidenceSnapshotRedactsAndGatesInvestigation(t *testing.T) {
	now := time.Now().UTC()
	incident, binding, items := evidenceFixture(now)
	limits := EvidenceLimits{MaxItems: 10, MaxSnippetBytes: 2048, MaxTotalBytes: 4096, MaxAge: time.Minute}
	snapshot, err := CaptureEvidence(context.Background(), fixedEvidenceLister(items), incident, now, limits)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.DeployedRevision != strings.Repeat("a", 40) || len(snapshot.SHA256) != 64 {
		t.Fatalf("snapshot lacks trusted revision or digest: %+v", snapshot)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"abc123", "alice@example.com", "raw-secret-metadata"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("snapshot leaked %q: %s", secret, encoded)
		}
	}
	if err := CheckInvestigationEvidence(incident, binding, snapshot, now); err != nil {
		t.Fatalf("valid evidence blocked: %v", err)
	}
	reordered := []domain.EvidenceItem{items[1], items[0]}
	again, err := BuildEvidenceSnapshot(incident, reordered, now, limits)
	if err != nil || again.SHA256 != snapshot.SHA256 {
		t.Fatalf("snapshot digest depended on input order: %q, %q, err %v", snapshot.SHA256, again.SHA256, err)
	}
}

func TestEvidenceSnapshotBlocksMissingStaleAndConflictingSignals(t *testing.T) {
	now := time.Now().UTC()
	incident, binding, items := evidenceFixture(now)
	limits := EvidenceLimits{MaxItems: 10, MaxSnippetBytes: 2048, MaxTotalBytes: 4096, MaxAge: time.Minute}
	for name, mutate := range map[string]func([]domain.EvidenceItem) []domain.EvidenceItem{
		"missing log": func(items []domain.EvidenceItem) []domain.EvidenceItem { return items[:1] },
		"stale log": func(items []domain.EvidenceItem) []domain.EvidenceItem {
			items[1].Timestamp = now.Add(-2 * time.Minute)
			return items
		},
		"missing revision": func(items []domain.EvidenceItem) []domain.EvidenceItem {
			items[0].MetadataJSON = `{}`
			return items
		},
		"conflicting revisions": func(items []domain.EvidenceItem) []domain.EvidenceItem {
			other := items[0]
			other.ID = "metric-2"
			other.MetadataJSON = strings.ReplaceAll(other.MetadataJSON, strings.Repeat("a", 40), strings.Repeat("b", 40))
			return append(items, other)
		},
	} {
		t.Run(name, func(t *testing.T) {
			copyItems := append([]domain.EvidenceItem(nil), items...)
			snapshot, err := BuildEvidenceSnapshot(incident, mutate(copyItems), now, limits)
			if err != nil {
				t.Fatal(err)
			}
			if err := CheckInvestigationEvidence(incident, binding, snapshot, now); err == nil {
				t.Fatal("accepted insufficient investigation evidence")
			}
		})
	}
	snapshot, err := BuildEvidenceSnapshot(incident, items, now, limits)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.DeployedRevision = strings.Repeat("f", 40)
	if err := CheckInvestigationEvidence(incident, binding, snapshot, now); err == nil {
		t.Fatal("accepted a modified evidence snapshot")
	}
	if _, err := BuildEvidenceSnapshot(incident, items, now, EvidenceLimits{MaxItems: 1, MaxSnippetBytes: 2048, MaxTotalBytes: 4096, MaxAge: time.Minute}); err == nil {
		t.Fatal("accepted evidence over item budget")
	}
}
