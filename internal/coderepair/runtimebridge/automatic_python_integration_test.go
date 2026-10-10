package runtimebridge

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/ai"
	"github.com/Cyaside/Triovexa/internal/ai/admission"
	"github.com/Cyaside/Triovexa/internal/ai/modelgateway"
	"github.com/Cyaside/Triovexa/internal/alerting"
	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/coderepair/agent"
	"github.com/Cyaside/Triovexa/internal/coderepair/publisher"
	"github.com/Cyaside/Triovexa/internal/config"
	"github.com/Cyaside/Triovexa/internal/domain"
	api "github.com/Cyaside/Triovexa/internal/http"
	"github.com/Cyaside/Triovexa/internal/incident"
	"github.com/Cyaside/Triovexa/internal/secretstore"
	"github.com/Cyaside/Triovexa/internal/storage/postgres"
	"github.com/google/uuid"
)

type pythonFixtureCollector struct{ revision string }

type forbiddenTriage struct{ t *testing.T }

func (f forbiddenTriage) Retrieve(context.Context, domain.Incident, []domain.EvidenceItem) ([]domain.DocumentReference, error) {
	f.t.Fatal("automatic investigation reached knowledge retrieval")
	return nil, nil
}

func (f forbiddenTriage) Generate(context.Context, domain.Incident, []domain.EvidenceItem, []domain.DocumentReference) (domain.TriageResult, error) {
	f.t.Fatal("automatic investigation consumed a triage model request")
	return domain.TriageResult{}, nil
}

func (c pythonFixtureCollector) Collect(_ context.Context, i domain.Incident) ([]domain.EvidenceItem, error) {
	metadata, _ := json.Marshal(map[string]any{"target": i.ServiceName, "complete": true, "deployed_revision": c.revision})
	now := time.Now().UTC()
	return []domain.EvidenceItem{
		{ID: uuid.NewString(), IncidentID: i.ID, Type: "log", Source: "fixture-logs", Timestamp: now, Snippet: "parser.py rejects supported value one", MetadataJSON: `{}`},
		{ID: uuid.NewString(), IncidentID: i.ID, Type: "metric", Source: "workload-control", Timestamp: now, Snippet: "queue backlog rising", MetadataJSON: string(metadata)},
	}, nil
}

// Actual HTTP intake, PostgreSQL, native DeepAgents and Docker tests. Model,
// alert telemetry and GitHub are explicitly offline fixtures; no paid API runs.
func TestAutomaticPythonAlertToDraftPRIntegration(t *testing.T) {
	image := os.Getenv("TEST_REPAIR_PYTHON_IMAGE")
	if image == "" {
		t.Skip("TEST_REPAIR_PYTHON_IMAGE is required")
	}
	f := persistedNativeFixture(t)
	root := t.TempDir()
	_ = os.Chmod(root, 0755)
	original, changed := "def accepts(value):\n    return value > 1\n", "def accepts(value):\n    return value > 0\n"
	for name, content := range map[string]string{
		"pyproject.toml":       "[project]\nname = 'python-fixture'\nversion = '0.1.0'\n",
		"parser.py":            original,
		"tests/test_parser.py": "import unittest\nfrom parser import accepts\n\nclass ParserTest(unittest.TestCase):\n    def test_accepts_one(self):\n        self.assertTrue(accepts(1), 'one rejected')\n    def test_rejects_zero(self):\n        self.assertFalse(accepts(0))\n",
	} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	fixtureGit(t, root, "init", "-b", "main")
	fixtureGit(t, root, "config", "core.autocrlf", "false")
	fixtureGit(t, root, "add", ".")
	fixtureGit(t, root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "test: reproduce Python boundary regression")
	base := fixtureGit(t, root, "rev-parse", "HEAD")
	_ = os.WriteFile(filepath.Join(root, "parser.py"), []byte(changed), 0644)
	patch := fixtureGit(t, root, "diff", "--", "parser.py") + "\n"
	fixtureGit(t, root, "checkout", "--", "parser.py")
	var calls atomic.Int32
	var evidenceIDs []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ordinal := int(calls.Add(1))
		var wire contextPreviewWire
		if json.NewDecoder(r.Body).Decode(&wire) != nil || r.Header.Get("Authorization") != "Bearer synthetic-python-key" {
			t.Error("invalid offline wire request")
		}
		var call contextPreviewCall
		switch ordinal {
		case 1:
			call = contextPreviewCall{"python-read", "repo_read", map[string]any{"path": "parser.py", "start_line": 1, "end_line": 20}}
		case 2:
			call = contextPreviewCall{"python-patch", "propose_patch", map[string]any{"patch": patch, "hypothesis": "The lower boundary rejects valid value one", "evidence_ids": evidenceIDs}}
		default:
			t.Error("unexpected additional model request")
			w.WriteHeader(500)
			return
		}
		_ = json.NewEncoder(w).Encode(contextPreviewCompletion(ordinal, []contextPreviewCall{call}))
	}))
	defer upstream.Close()
	client, err := ai.NewOpenAICompatibleClient(ai.ProviderConfig{Name: "openai-compatible", BaseURL: upstream.URL, Model: "glm-5.3-flash", APIKey: "synthetic-python-key", AllowHTTP: true, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	cipher, _ := secretstore.NewCipher("", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	// Automatic/internal investigations expose the complete bounded tool set.
	// These synthetic limits do not modify the retained final-smoke campaign.
	budget := modelgateway.BudgetConfig{Campaign: admission.Campaign{ID: "python-auto-" + uuid.NewString(), Profile: "offline-fixture", Offline: true, MaxRequests: 6, MaxSpendMicroUSD: 200000, MaxInputTokens: 24000},
		Pricing: admission.Pricing{Version: "synthetic-python-v1", Provider: "openai-compatible", Model: "glm-5.3-flash", InputContract: modelgateway.GLMFlashInputContract}, ConfigVersion: "python-fixture-v1", MaxInputTokens: 12000, MaxOutputTokens: 1500}
	selection, err := SealSelection(client, budget, cipher)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	if err := f.store.CreateUser(ctx, domain.User{ID: "fixture-admin", Username: "fixture-admin", PasswordHash: "synthetic", Role: domain.RoleAdmin, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	b := coderepair.RepositoryBinding{ID: uuid.NewString(), ServiceName: "python-worker", Environment: "staging", RepositoryURL: "https://github.com/owner/python-worker", BaseRef: "main", Enabled: true, PolicyVersion: "repair-v2", CreatedAt: now, UpdatedAt: now,
		AllowedPaths: []string{"parser.py", "tests", "pyproject.toml"}, TestRecipes: []string{"python-tests"},
		Automation:        &coderepair.AutomationPolicy{Enabled: true, AuthorizedBy: "fixture-admin", ExpiresAt: now.Add(15 * time.Minute), CampaignID: budget.Campaign.ID, MaxInvestigations: 1, MaxModelRequests: 2, PublishDraftPR: true},
		ValidationProfile: &coderepair.ValidationProfile{ID: "python-tests", Version: "python-v1", Image: image, RootFiles: []string{"pyproject.toml"}, ProtectedPaths: []string{"tests"}, Test: coderepair.ValidationCommand{Executable: "/usr/local/bin/python", Arguments: []string{"-m", "unittest", "discover", "-s", "tests", "-v"}, TimeoutSeconds: 30}, ExpectedTestName: "test_accepts_one", ExpectedFailure: "one rejected"}}
	if err := f.store.CreateRepositoryBinding(ctx, b); err != nil {
		t.Fatal(err)
	}
	dispatcher := &coderepair.AutomaticDispatcher{Store: f.store, ResolveBase: func(context.Context, coderepair.RepositoryBinding) (string, error) { return base, nil }, Allowed: func(context.Context) error { return nil }, Selection: func() (coderepair.AgentSelection, error) { return selection, nil }}
	service := incident.NewService(f.store, pythonFixtureCollector{base}, forbiddenTriage{t}, forbiddenTriage{t}, nil, nil).WithAutomaticInvestigation(dispatcher.Try)
	server := httptest.NewServer(api.NewServerWithTelemetry(config.Config{DeploymentMode: "local-demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), f.store, service, nil, nil, nil).Handler)
	defer server.Close()
	payload, _ := json.Marshal(alerting.GrafanaWebhookPayload{Title: "Python job parser rejects supported jobs", State: "firing", Alerts: []alerting.GrafanaAlert{{Fingerprint: "python-regression", Status: "firing", StartsAt: now, Labels: map[string]string{"service": b.ServiceName, "environment": b.Environment, "severity": "high"}}}})
	for range 2 {
		response, err := http.Post(server.URL+"/webhooks/grafana", "application/json", bytes.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusAccepted {
			t.Fatalf("alert intake: %d", response.StatusCode)
		}
	}
	triage, err := f.store.ClaimJob(ctx, "python-alert", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ProcessWorkflowJob(ctx, triage); err != nil {
		t.Fatal(err)
	}
	if err := f.store.CompleteJob(ctx, triage.ID, "python-alert"); err != nil {
		t.Fatal(err)
	}
	job, err := f.store.ClaimRepairJob(ctx, "python-agent", time.Now().UTC(), 8*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.store.GetRepairEvidenceSnapshot(ctx, job.CaseID)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range snapshot.Entries {
		evidenceIDs = append(evidenceIDs, item.ID)
	}
	ledger, _ := admission.NewService(f.store.ModelBudgetStore())
	tests := &persistedDockerTester{image: image}
	runner := &NativeRunner{Process: Process{Executable: f.node, Entry: f.entry, Version: EngineVersion}, Tests: tests, Store: f.store, Ledger: ledger, Cipher: cipher, CheckpointDSN: f.checkpointDSN, CheckpointSchema: f.checkpoint}
	checkout := func(_ context.Context, _ coderepair.RepositoryBinding, revision string) (string, func() error, error) {
		if revision != base {
			t.Fatal("checkout escaped the pinned base")
		}
		copyRoot := filepath.Join(t.TempDir(), "checkout")
		fixtureGit(t, root, "clone", "--no-hardlinks", "--no-checkout", "--", root, copyRoot)
		fixtureGit(t, copyRoot, "config", "core.autocrlf", "false")
		fixtureGit(t, copyRoot, "checkout", "--detach", base)
		return copyRoot, func() error { return nil }, nil
	}
	handler, err := agent.NewHandler(f.store, runner, checkout)
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.Handle(ctx, job); err != nil {
		t.Fatal(err)
	}
	if ok, err := f.store.CompleteRepairJob(ctx, job.ID, job.LeaseToken, time.Now().UTC()); err != nil || !ok {
		t.Fatalf("complete repair: %v %v", ok, err)
	}
	p, err := f.store.ClaimRepairPublication(ctx, "python-publisher", time.Now().UTC(), time.Minute)
	if err != nil {
		a, _ := f.store.GetRepairAttempt(ctx, job.AttemptID)
		t.Fatalf("publication not queued: %v; investigation=%s %s", err, a.ErrorCode, a.ErrorMessage)
	}
	input, err := f.store.GetRepairPublicationInput(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	github, remote := newLocalGitHubFileFixture(t, input.Binding, p, base, "parser.py", original, changed)
	publisherService, _ := publisher.NewService(github, publisher.Checkout(checkout), func(ctx context.Context) error {
		return f.store.CheckRepairPublicationSafety(ctx, p.CaseID, time.Now().UTC())
	})
	pr, head, err := publisherService.Publish(ctx, p, publisher.Input(input), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	// Reopen the store before committing the remote result, simulating a crash
	// after PR creation. Existing() must reconcile without another GitHub write.
	reopened, err := postgres.NewPostgresStore(f.appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if repeated, _, err := publisherService.Publish(ctx, p, publisher.Input(input), time.Now().UTC()); err != nil || repeated.Number != pr.Number {
		t.Fatalf("PR replay: %v", err)
	}
	if ok, err := reopened.CompleteRepairPublication(ctx, p, head, pr.Number, pr.URL, time.Now().UTC()); err != nil || !ok {
		t.Fatalf("publication outcome: %v %v", ok, err)
	}
	final, _ := reopened.GetRepairCase(ctx, p.CaseID)
	if final.State != coderepair.StatePROpen || calls.Load() != 2 || tests.calls != 2 || remote.branchWrites != 1 || remote.prWrites != 1 || input.Approval.Phase != "automatic_publication" {
		t.Fatalf("chain: state=%s calls=%d tests=%d branches=%d PRs=%d", final.State, calls.Load(), tests.calls, remote.branchWrites, remote.prWrites)
	}
	t.Logf("offline Python alert->native patch->real Docker regression->draft PR: model=%d tests=%d branch=%d PR=%d; no paid calls", calls.Load(), tests.calls, remote.branchWrites, remote.prWrites)
}
