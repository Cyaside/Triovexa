package observability

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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
		_, _ = fmt.Fprintf(w, `{"target":"queue-worker","source":"redis-streams","timestamp":%q,"complete":true,"worker_healthy":false,"consumer_paused":false,"queue_backlog":42,"jobs_processed":7,"jobs_produced":49,"errors":0,"generation":2}`, time.Now().UTC().Format(time.RFC3339Nano))
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
}
