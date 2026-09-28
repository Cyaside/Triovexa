package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/domain"
)

func TestWorkloadCollectorReturnsNeutralEvidence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatal("missing control credential")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"target":"queue-worker","source":"redis-streams","timestamp":%q,"complete":true,"worker_healthy":false,"consumer_paused":false,"queue_backlog":42,"jobs_processed":7,"jobs_produced":49,"errors":0,"generation":2,"deployed_revision":%q}`, time.Now().UTC().Format(time.RFC3339Nano), strings.Repeat("a", 40))
	}))
	defer server.Close()

	items, err := NewWorkloadCollector(server.URL, "secret").Collect(context.Background(), domain.Incident{ID: "incident-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Source != "workload-control" {
		t.Fatalf("unexpected evidence: %#v", items)
	}
	if items[0].Timestamp.IsZero() || items[0].MetadataJSON == "" {
		t.Fatalf("evidence lacks timestamp or metadata: %#v", items[0])
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(items[0].MetadataJSON), &metadata); err != nil || metadata["deployed_revision"] != strings.Repeat("a", 40) {
		t.Fatalf("evidence lacks deployed revision: %#v, err %v", metadata, err)
	}
}
