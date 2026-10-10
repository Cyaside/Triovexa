//go:build provider_smoke

package runtimebridge

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/coderepair/sandbox"
	"github.com/Cyaside/Triovexa/internal/config"
	"github.com/Cyaside/Triovexa/internal/domain"
	api "github.com/Cyaside/Triovexa/internal/http"
	"github.com/Cyaside/Triovexa/internal/incident"
	"github.com/google/uuid"
)

type smokeInvestigationFixture struct {
	workspace    *sandbox.Workspace
	binding      coderepair.RepositoryBinding
	base, recipe string
	job          coderepair.Job
	repairCase   coderepair.Case
	attempt      coderepair.Attempt
	snapshot     coderepair.EvidenceSnapshot
}

type repositorySmokeConfig struct {
	Binding                                                                                                   coderepair.RepositoryBinding
	BaseSHA, ApplicationURL, PrometheusURL, ContainerName, ValidationID, WebhookListenAddress, WebhookKeyPath string
}

func readRepositorySmokeFailure(t *testing.T, repo, path, output string) reviewedProviderSmokeAttempt {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || !filepath.IsAbs(path) || path == output {
		t.Fatal("retained failure path is invalid")
	}
	relative, err := filepath.Rel(filepath.Join(repo, "artifacts"), resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		t.Fatal("retained failure must remain in private artifacts")
	}
	file, err := os.Open(resolved)
	if err != nil {
		t.Fatal("retained failure proof is unavailable")
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 65537))
	var report reviewedProviderSmokeAttempt
	if err != nil || len(raw) > 65536 || json.Unmarshal(raw, &report) != nil {
		t.Fatal("retained failure proof is unavailable")
	}
	return report
}

func prepareProviderSmokeFixture(t *testing.T, ctx context.Context, f persistedFixture, checkout string,
	selection coderepair.AgentSelection, real bool, proof map[string]any) smokeInvestigationFixture {
	t.Helper()
	if !real {
		w, binding, base := compatibilityFixtureAt(t, checkout)
		job, c, attempt, snapshot := approveNativeFixture(t, f.store, binding, base, strings.Repeat("a", 40), selection)
		return smokeInvestigationFixture{w, binding, base, "go-test-workload", job, c, attempt, snapshot}
	}
	path := os.Getenv("LIVE_REPOSITORY_CONFIG_PATH")
	info, err := os.Lstat(path)
	if err != nil || !filepath.IsAbs(path) || !info.Mode().IsRegular() || info.Size() > 65536 {
		t.Fatal("explicit local repository test configuration is required")
	}
	raw, err := os.ReadFile(path)
	var cfg repositorySmokeConfig
	if err != nil || json.Unmarshal(raw, &cfg) != nil || !strings.HasPrefix(cfg.Binding.BaseRef, "triovexa-test/") ||
		cfg.Binding.Environment != "isolated-test" || cfg.Binding.Automation != nil || cfg.Binding.ValidationProfile == nil ||
		cfg.Binding.ValidationProfile.Image != f.image || cfg.Binding.CredentialRef != "env:REPAIR_GITHUB_READ_TOKEN" ||
		cfg.ValidationID == "" || !coderepair.ValidGitRevision(cfg.BaseSHA) {
		t.Fatal("isolated repository binding is invalid")
	}
	for _, endpoint := range []string{cfg.ApplicationURL, cfg.PrometheusURL} {
		u, err := url.Parse(endpoint)
		if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			t.Fatal("live test telemetry must use explicitly selected loopback services")
		}
	}
	keyInfo, err := os.Lstat(cfg.WebhookKeyPath)
	if err != nil || !filepath.IsAbs(cfg.WebhookKeyPath) || !keyInfo.Mode().IsRegular() || keyInfo.Size() > 128 {
		t.Fatal("isolated webhook credential path is invalid")
	}
	key, err := os.ReadFile(cfg.WebhookKeyPath)
	if err != nil || len(key) < 32 || len(key) > 128 {
		t.Fatal("isolated webhook credential is unavailable")
	}
	now := time.Now().UTC()
	cfg.Binding.ID, cfg.Binding.CreatedAt, cfg.Binding.UpdatedAt = uuid.NewString(), now, now
	if err := cfg.Binding.Validate(); err != nil {
		t.Fatal("isolated repository scope failed validation")
	}
	collector := &repositorySmokeCollector{cfg: cfg}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, cfg.ApplicationURL+"/healthz", nil)
	response, err := (&http.Client{Timeout: 3 * time.Second}).Do(request)
	if err != nil {
		t.Fatal("isolated application is unavailable")
	}
	_ = response.Body.Close()
	if response.StatusCode != 500 {
		t.Fatal("source regression is not active")
	}
	// Verify image/deployment provenance and the registered remote revision before
	// opening the webhook listener. Alert labels cannot supply trusted provenance.
	if _, err := collector.collect(ctx, "preflight"); err != nil {
		t.Fatal(err)
	}
	base, err := sandbox.ResolveBaseRevision(ctx, cfg.Binding)
	if err != nil || base != cfg.BaseSHA {
		t.Fatal("registered test branch moved before checkout")
	}
	root, err := sandbox.Checkout(ctx, checkout, cfg.Binding, base)
	if err != nil {
		t.Fatal("authenticated private checkout failed: ", err)
	}
	w, err := sandbox.Open(root, cfg.Binding, sandbox.Limits{MaxFileBytes: 32768, MaxTotalBytes: 204800, MaxListedFiles: 40, MaxSearchHits: 40})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	if err := f.store.CreateRepositoryBinding(ctx, cfg.Binding); err != nil {
		t.Fatal(err)
	}
	const actor = "isolated-test-operator"
	if err := f.store.CreateUser(ctx, domain.User{ID: actor, Username: actor, PasswordHash: "no-interactive-login", Role: domain.RoleAdmin, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	service := incident.NewService(f.store, collector, nil, nil, nil, nil)
	listener, err := net.Listen("tcp", cfg.WebhookListenAddress)
	if err != nil {
		t.Fatal("isolated webhook listener is unavailable")
	}
	router := api.NewServerWithTelemetry(config.Config{DeploymentMode: "local-demo", GrafanaWebhookSecret: string(key)},
		slog.New(slog.NewTextHandler(io.Discard, nil)), f.store, service, nil, nil, nil).Handler
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/webhooks/grafana" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		router.ServeHTTP(w, r)
	}), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	// Generate real failing requests. Prometheus evaluates its provisioned rule;
	// Alertmanager delivers it to the same production HTTP intake handler.
	var record domain.Incident
	deadline := time.Now().Add(50 * time.Second)
	for time.Now().Before(deadline) {
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, cfg.ApplicationURL+"/healthz", nil)
		response, err := (&http.Client{Timeout: 3 * time.Second}).Do(request)
		if err != nil {
			t.Fatal("isolated application request failed")
		}
		_ = response.Body.Close()
		if response.StatusCode != 500 {
			t.Fatal("the source regression was not reproduced")
		}
		items, err := f.store.ListIncidents(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) > 0 {
			if len(items) != 1 || items[0].ServiceName != cfg.Binding.ServiceName || items[0].Environment != cfg.Binding.Environment {
				t.Fatal("received alert does not match the isolated target")
			}
			record = items[0]
			break
		}
		time.Sleep(2 * time.Second)
	}
	if record.ID == "" {
		t.Fatal("Prometheus/Alertmanager did not deliver the real alert")
	}
	proof["alert_source"], proof["incident_id"], proof["repository_url"], proof["base_ref"] = "prometheus-alertmanager", record.ID, cfg.Binding.RepositoryURL, cfg.Binding.BaseRef
	proof["provenance_source"], proof["real_deployments"], proof["production_deployments"] = "operator-controlled-docker-image-label", 1, 0
	proof["retained_checkout"] = root
	items, err := collector.Collect(ctx, record)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.SaveEvidenceItems(ctx, items); err != nil {
		t.Fatal(err)
	}
	// The final-smoke profile cannot auto-dispatch alerts. This explicit operator
	// escalation authorizes one manual investigation, with no heuristic triage.
	if err := f.store.UpdateIncidentState(ctx, record.ID, domain.IncidentStateEscalated); err != nil {
		t.Fatal(err)
	}
	if err := f.store.AddAuditEvent(ctx, domain.AuditEvent{ID: uuid.NewString(), IncidentID: record.ID, StepName: "operator_investigation_requested", Status: "accepted", DetailsJSON: `{"mode":"bounded-final-validation","actor_id":"isolated-test-operator"}`, StartedAt: time.Now().UTC(), FinishedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	proposal, err := coderepair.NewProposalService(f.store, sandbox.ResolveBaseRevision, coderepair.EvidenceLimits{MaxItems: 4, MaxSnippetBytes: 2048, MaxTotalBytes: 4096, MaxAge: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	c, snapshot, err := proposal.Propose(ctx, record.ID, actor, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	authorizer, err := coderepair.NewInvestigationAuthorizationService(f.store)
	if err != nil {
		t.Fatal(err)
	}
	_, attempt, _, err := authorizer.Authorize(ctx, c.ID, actor, selection, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	job, err := f.store.ClaimRepairJob(ctx, "native-repository-smoke", time.Now().UTC(), 8*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	c, err = f.store.GetRepairCase(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err = f.store.GetRepairAttempt(ctx, attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	return smokeInvestigationFixture{w, cfg.Binding, base, cfg.Binding.ValidationProfile.ID, job, c, attempt, snapshot}
}
