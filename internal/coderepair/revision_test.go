package coderepair

import (
	"strings"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/domain"
)

func TestTrustedDeployedRevisionRejectsUntrustedOrStaleEvidence(t *testing.T) {
	now := time.Now().UTC()
	revision := strings.Repeat("a", 40)
	incident := domain.Incident{ID: "incident-1", ServiceName: "queue-worker"}
	valid := domain.EvidenceItem{
		ID: "evidence-1", IncidentID: incident.ID, Type: "metric", Source: "workload-control",
		Timestamp: now, MetadataJSON: `{"target":"queue-worker","complete":true,"deployed_revision":"` + revision + `"}`,
	}
	if got, err := TrustedDeployedRevision(incident, []domain.EvidenceItem{valid}, now); err != nil || got != revision {
		t.Fatalf("trusted revision: got %q, err %v", got, err)
	}
	for name, change := range map[string]func(*domain.EvidenceItem){
		"alert source": func(item *domain.EvidenceItem) { item.Source = "prometheus-alert" },
		"wrong target": func(item *domain.EvidenceItem) {
			item.MetadataJSON = strings.ReplaceAll(item.MetadataJSON, "queue-worker", "other-worker")
		},
		"incomplete": func(item *domain.EvidenceItem) {
			item.MetadataJSON = strings.ReplaceAll(item.MetadataJSON, "true", "false")
		},
		"stale": func(item *domain.EvidenceItem) { item.Timestamp = now.Add(-2 * time.Minute) },
		"malformed hash": func(item *domain.EvidenceItem) {
			item.MetadataJSON = strings.ReplaceAll(item.MetadataJSON, revision, "not-a-sha")
		},
		"wrong incident":   func(item *domain.EvidenceItem) { item.IncidentID = "incident-2" },
		"future timestamp": func(item *domain.EvidenceItem) { item.Timestamp = now.Add(time.Minute) },
	} {
		t.Run(name, func(t *testing.T) {
			item := valid
			change(&item)
			if got, err := TrustedDeployedRevision(incident, []domain.EvidenceItem{item}, now); err == nil || got != "" {
				t.Fatalf("accepted untrusted revision: got %q, err %v", got, err)
			}
		})
	}
	conflict := valid
	conflict.ID = "evidence-2"
	conflict.MetadataJSON = strings.ReplaceAll(conflict.MetadataJSON, revision, strings.Repeat("b", 40))
	if _, err := TrustedDeployedRevision(incident, []domain.EvidenceItem{valid, conflict}, now); err == nil {
		t.Fatal("accepted conflicting revisions")
	}
}
