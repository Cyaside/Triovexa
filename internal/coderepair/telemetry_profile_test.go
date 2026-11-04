package coderepair

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/domain"
)

type telemetryEvidence []domain.EvidenceItem

func (e telemetryEvidence) ListEvidenceItems(context.Context, string) ([]domain.EvidenceItem, error) {
	return e, nil
}

func TestHTTPProfileRequiresFreshPinnedSourceRevisionAndTrustedLogs(t *testing.T) {
	profile := &TelemetryProfile{Version: TelemetryVersion, Kind: "http", AlertName: "HTTPServerErrors", LogSources: []string{"grafana-loki"}, HTTP: &HTTPSignals{RequestsMetric: "http_requests_total", HealthMetric: "up", RevisionMetric: "build_info", RevisionLabel: "revision", StatusLabel: "status", ServiceLabel: "service", EnvironmentLabel: "environment", WindowSeconds: 30, MinimumRequests: 1, MaxErrorRatio: 0.01}}
	b := RepositoryBinding{ID: "binding", ServiceName: "api", Environment: "staging", RepositoryURL: "https://github.com/acme/api", BaseRef: "main", AllowedPaths: []string{"src"}, TestRecipes: []string{"test-api"}, PolicyVersion: "repair-v3", Enabled: true, TelemetryProfile: profile}
	now := time.Now().UTC()
	incident := domain.Incident{ID: "incident", ServiceName: "api", Environment: "staging", State: domain.IncidentStateEscalated}
	metadata, _ := json.Marshal(map[string]any{"target": "api", "environment": "staging", "complete": true, "deployed_revision": strings.Repeat("1", 40), "profile_sha256": b.TelemetryContract().Digest()})
	evidence := telemetryEvidence{{ID: "metric", IncidentID: incident.ID, Type: "metric", Source: "repair-prometheus", Timestamp: now, Snippet: "HTTP 500 responses", MetadataJSON: string(metadata)}, {ID: "log", IncidentID: incident.ID, Type: "log", Source: "grafana-loki", Timestamp: now, Snippet: "RuntimeError in healthz"}}
	snapshot, err := CaptureEvidenceForBinding(t.Context(), evidence, incident, b, now, EvidenceLimits{MaxItems: 10, MaxSnippetBytes: 1024, MaxTotalBytes: 2048, MaxAge: time.Minute})
	if err != nil || CheckInvestigationEvidence(incident, b, snapshot, now) != nil || snapshot.DeployedRevision != strings.Repeat("1", 40) {
		t.Fatalf("HTTP evidence rejected: %v", err)
	}
	for _, scenario := range []string{"wrong source", "wrong environment", "stale revision", "untrusted log", "conflicting revision"} {
		t.Run(scenario, func(t *testing.T) {
			items := append(telemetryEvidence(nil), evidence...)
			switch scenario {
			case "wrong source":
				items[0].Source = "alert-payload"
			case "wrong environment":
				items[0].MetadataJSON = strings.Replace(items[0].MetadataJSON, "staging", "production", 1)
			case "stale revision":
				items[0].Timestamp = now.Add(-2 * time.Minute)
			case "untrusted log":
				items[1].Source = "alert-payload"
			case "conflicting revision":
				other := items[0]
				other.ID = "other"
				other.MetadataJSON = strings.Replace(other.MetadataJSON, strings.Repeat("1", 40), strings.Repeat("2", 40), 1)
				items = append(items, other)
			}
			s, err := CaptureEvidenceForBinding(t.Context(), items, incident, b, now, EvidenceLimits{MaxItems: 10, MaxSnippetBytes: 1024, MaxTotalBytes: 2048, MaxAge: time.Minute})
			if err == nil && CheckInvestigationEvidence(incident, b, s, now) == nil {
				t.Fatal("untrusted HTTP evidence accepted")
			}
		})
	}
	c := Case{ID: "case", IncidentID: incident.ID, BindingID: b.ID, BaseSHA: strings.Repeat("1", 40), DeployedSHA: strings.Repeat("1", 40), PolicyVersion: b.PolicyVersion}
	before, _ := ScopeDigest(c, b)
	profile.HTTP.MaxErrorRatio = 0.5
	after, _ := ScopeDigest(c, b)
	if before == after || CheckInvestigationEvidence(incident, b, snapshot, now) == nil {
		t.Fatal("changed telemetry policy did not invalidate scope/snapshot")
	}
}
