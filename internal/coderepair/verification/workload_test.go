package verification

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestRecoveryRequiresDeployedRevisionAndMeasuredProgress(t *testing.T) {
	now := time.Now().UTC()
	oldSHA := "1111111111111111111111111111111111111111"
	newSHA := "2222222222222222222222222222222222222222"
	before := Sample{Target: "queue-worker", Source: "redis-streams", Timestamp: now,
		Complete: true, AlertObserved: true, QueueBacklog: 100, JobsProcessed: 10, Errors: 4, DeployedRevision: oldSHA}
	after := Sample{Target: "queue-worker", Source: "redis-streams", Timestamp: now.Add(10 * time.Second),
		Complete: true, AlertObserved: true, AlertCleared: true, WorkerHealthy: true, QueueBacklog: 70, JobsProcessed: 40, Errors: 4, DeployedRevision: newSHA}
	if !Check(before, after, newSHA) {
		t.Fatal("verified recovery rejected")
	}
	cases := map[string]Sample{
		"wrong revision":  func() Sample { s := after; s.DeployedRevision = oldSHA; return s }(),
		"no progress":     func() Sample { s := after; s.JobsProcessed = before.JobsProcessed; return s }(),
		"backlog stuck":   func() Sample { s := after; s.QueueBacklog = before.QueueBacklog; return s }(),
		"errors worsened": func() Sample { s := after; s.Errors++; return s }(),
		"paused":          func() Sample { s := after; s.ConsumerPaused = true; return s }(),
		"alert firing":    func() Sample { s := after; s.AlertCleared = false; return s }(),
	}
	for name, sample := range cases {
		t.Run(name, func(t *testing.T) {
			if Check(before, sample, newSHA) {
				t.Fatal("false recovery")
			}
		})
	}
}

func TestWorkloadClientRequiresPrometheusAlertClearance(t *testing.T) {
	var firing atomic.Bool
	var rulePresent atomic.Bool
	firing.Store(true)
	rulePresent.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/state":
			if r.Header.Get("Authorization") != "Bearer control-token" {
				w.WriteHeader(401)
				return
			}
			fmt.Fprintf(w, `{"target":"queue-worker","source":"redis-streams","timestamp":%q,"complete":true,"worker_healthy":true,"queue_backlog":0,"jobs_processed":42,"errors":1,"deployed_revision":"2222222222222222222222222222222222222222"}`, time.Now().UTC().Format(time.RFC3339Nano))
		case "/api/v1/alerts":
			if firing.Load() {
				fmt.Fprint(w, `{"status":"success","data":{"alerts":[{"labels":{"alertname":"TriovexaQueueBacklogHigh"},"state":"firing"}]}}`)
			} else {
				fmt.Fprint(w, `{"status":"success","data":{"alerts":[]}}`)
			}
		case "/api/v1/rules":
			if rulePresent.Load() {
				fmt.Fprint(w, `{"status":"success","data":{"groups":[{"rules":[{"name":"TriovexaQueueBacklogHigh","type":"alerting","health":"ok"}]}]}}`)
			} else {
				fmt.Fprint(w, `{"status":"success","data":{"groups":[]}}`)
			}
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	client, err := NewWorkloadClient(server.URL, "control-token", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	first, err := client.Snapshot(t.Context())
	if err != nil || !first.AlertObserved || first.AlertCleared {
		t.Fatalf("firing alert missed: %+v %v", first, err)
	}
	firing.Store(false)
	second, err := client.Snapshot(t.Context())
	if err != nil || !second.AlertCleared {
		t.Fatalf("cleared alert missed: %+v %v", second, err)
	}
	rulePresent.Store(false)
	if _, err := client.Snapshot(t.Context()); err == nil {
		t.Fatal("a missing alert rule was treated as recovery")
	}
}

func TestWorkloadClientRejectsAmbiguousEndpoints(t *testing.T) {
	for _, endpoint := range []string{"http://", "http://user:pass@localhost:8080", "http://localhost:8080/?token=secret", "http://localhost:8080/#fragment"} {
		if _, err := NewWorkloadClient(endpoint, "token", "http://localhost:9090"); err == nil {
			t.Fatalf("accepted invalid workload URL %q", endpoint)
		}
	}
}
