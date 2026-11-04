package verification

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Real PromQL evaluation proves freshness filtering rather than trusting a
// query API's evaluation timestamp as the original observation timestamp.
func TestHTTPRecoveryQueriesPrometheusIntegration(t *testing.T) {
	image := os.Getenv("TEST_PROMETHEUS_IMAGE")
	if image == "" {
		t.Skip("TEST_PROMETHEUS_IMAGE is required")
	}
	c := httpContract()
	q, err := RenderHTTPQueries(*c)
	if err != nil {
		t.Fatal(err)
	}
	spec := map[string]any{"evaluation_interval": "10s", "tests": []any{map[string]any{
		"interval": "10s",
		"input_series": []any{
			map[string]string{"series": `http_requests_total{service="api",environment="staging",path="/healthz",status="500"}`, "values": "0+10x6"},
			map[string]string{"series": `probe_success{service="api",environment="staging"}`, "values": "0+0x6"},
			map[string]string{"series": `app_build_info{service="api",environment="staging",revision="` + strings.Repeat("1", 40) + `"}`, "values": "1+0x6"},
		},
		"promql_expr_test": []any{
			map[string]any{"expr": q.Requests, "eval_time": "60s", "exp_samples": []any{map[string]any{"labels": `{service="api",environment="staging",status="500"}`, "value": 30}}},
			map[string]any{"expr": q.Health, "eval_time": "60s", "exp_samples": []any{map[string]any{"labels": `probe_success{service="api",environment="staging"}`, "value": 0}}},
			map[string]any{"expr": q.Revision, "eval_time": "60s", "exp_samples": []any{map[string]any{"labels": `app_build_info{service="api",environment="staging",revision="` + strings.Repeat("1", 40) + `"}`, "value": 1}}},
			map[string]any{"expr": q.Health, "eval_time": "130s", "exp_samples": []any{}},
			map[string]any{"expr": q.Revision, "eval_time": "130s", "exp_samples": []any{}},
		},
	}}}
	root := t.TempDir()
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "queries.json"), data, 0644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	args := []string{"run", "--rm", "--network", "none", "--read-only", "--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=64m,mode=1777", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit=64", "--memory=256m", "--cpus=1", "--mount", "type=bind,src=" + root + ",dst=/fixture,readonly", "--entrypoint", "/bin/promtool", image, "test", "rules", "/fixture/queries.json"}
	if output, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
		t.Fatalf("real Prometheus query evaluation failed: %v\n%s", err, output)
	}
}
