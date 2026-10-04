package runtimebridge

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/storage/postgres"
	"github.com/google/uuid"
)

type persistedFixture struct {
	admin         *sql.DB
	store         *postgres.PostgresStore
	appDSN        string
	checkpointDSN string
	checkpoint    string
	node          string
	entry         string
	image         string
	appSchema     string
	role          string
}

func persistedNativeFixture(t *testing.T) persistedFixture {
	return persistedNativeFixtureRetained(t, false, nil)
}

func persistedNativeFixtureRetained(t *testing.T, retain bool, recordAllocation func(appSchema, checkpoint, role string)) persistedFixture {
	t.Helper()
	dsn, entry, image := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_AGENT_RUNTIME_ENTRY"), os.Getenv("TEST_REPAIR_SANDBOX_IMAGE")
	if dsn == "" || entry == "" || image == "" {
		t.Skip("dedicated TEST_DATABASE_URL, built TEST_AGENT_RUNTIME_ENTRY and TEST_REPAIR_SANDBOX_IMAGE are required")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || !strings.Contains(parsed.Path, "test") || (parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost" && parsed.Hostname() != "::1") {
		t.Fatal("native integration requires a loopback database explicitly named for tests")
	}
	if !filepath.IsAbs(entry) {
		t.Fatal("native integration entry must be the absolute built runtime path")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("built native runtime requires Node")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatal("native integration requires the existing Docker runtime")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal("test PostgreSQL connection could not be opened")
	}
	t.Cleanup(func() { _ = admin.Close() })
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	appSchema, checkpoint, role := "native_app_"+suffix, "native_checkpoint_"+suffix, "native_role_"+suffix
	if recordAllocation != nil {
		recordAllocation(appSchema, checkpoint, role)
	}
	if _, err := admin.Exec(`CREATE SCHEMA ` + appSchema); err != nil {
		t.Fatal(err)
	}
	if !retain {
		t.Cleanup(func() {
			_, _ = admin.Exec(`DROP SCHEMA IF EXISTS ` + appSchema + ` CASCADE`)
			_, _ = admin.Exec(`DROP SCHEMA IF EXISTS ` + checkpoint + ` CASCADE`)
			_, _ = admin.Exec(`DROP ROLE IF EXISTS ` + role)
		})
	}
	query := parsed.Query()
	query.Set("search_path", appSchema)
	parsed.RawQuery = query.Encode()
	appDSN := parsed.String()
	store, err := postgres.NewPostgresStore(appDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	initEntry := filepath.Join(filepath.Dir(entry), "checkpoints", "init.js")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, initEntry)
	for _, name := range []string{"PATH", "SystemRoot", "WINDIR", "TEMP", "TMP", "TMPDIR"} {
		if value, exists := os.LookupEnv(name); exists {
			command.Env = append(command.Env, name+"="+value)
		}
	}
	const password = "synthetic-native-checkpoint-password"
	command.Env = append(command.Env, "TRIOVEXA_CHECKPOINT_ADMIN_DSN="+dsn, "TRIOVEXA_CHECKPOINT_SCHEMA="+checkpoint,
		"TRIOVEXA_CHECKPOINT_ROLE="+role, "TRIOVEXA_CHECKPOINT_ROLE_PASSWORD="+password,
		"TRIOVEXA_CHECKPOINT_HARDEN_DEDICATED_DATABASE=true")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated checkpoint bootstrap failed: %s", output)
	}
	runtimeURL, _ := url.Parse(dsn)
	runtimeURL.User = url.UserPassword(role, password)
	return persistedFixture{admin: admin, store: store, appDSN: appDSN, checkpointDSN: runtimeURL.String(), checkpoint: checkpoint, node: node, entry: entry, image: image, appSchema: appSchema, role: role}
}

func approveNativeFixture(t *testing.T, store *postgres.PostgresStore, binding coderepair.RepositoryBinding,
	base, deployed string, selection coderepair.AgentSelection) (coderepair.Job, coderepair.Case, coderepair.Attempt, coderepair.EvidenceSnapshot) {
	t.Helper()
	ctx, now := context.Background(), time.Now().UTC()
	incident := domain.Incident{ID: uuid.NewString(), ExternalAlertID: uuid.NewString(), AlertSource: "native-integration",
		Title: "schema two jobs rejected", ServiceName: binding.ServiceName, Environment: binding.Environment,
		Severity: "high", State: domain.IncidentStateEscalated, CreatedAt: now, UpdatedAt: now}
	if err := store.CreateIncident(ctx, incident); err != nil {
		t.Fatal(err)
	}
	binding.ID, binding.CreatedAt, binding.UpdatedAt = uuid.NewString(), now, now
	if err := store.CreateRepositoryBinding(ctx, binding); err != nil {
		t.Fatal(err)
	}
	metadata, _ := json.Marshal(map[string]any{"target": binding.ServiceName, "complete": true, "deployed_revision": deployed})
	evidence := []domain.EvidenceItem{
		{ID: uuid.NewString(), IncidentID: incident.ID, Type: "log", Source: "fixture-logs", Timestamp: now, Snippet: "valid schema two jobs are rejected", MetadataJSON: `{}`},
		{ID: uuid.NewString(), IncidentID: incident.ID, Type: "metric", Source: "workload-control", Timestamp: now, Snippet: "backlog increasing", MetadataJSON: string(metadata)},
	}
	if err := store.SaveEvidenceItems(ctx, evidence); err != nil {
		t.Fatal(err)
	}
	snapshot, err := coderepair.BuildEvidenceSnapshot(incident, evidence, now, coderepair.EvidenceLimits{MaxItems: 4, MaxSnippetBytes: 1024, MaxTotalBytes: 2048, MaxAge: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	repairCase := coderepair.Case{ID: uuid.NewString(), IncidentID: incident.ID, BindingID: binding.ID, BaseSHA: base, DeployedSHA: deployed,
		PolicyVersion: binding.PolicyVersion, State: coderepair.StateAwaitingInvestigationApproval, Version: 1, CreatedBy: "fixture-operator", CreatedAt: now, UpdatedAt: now}
	repairCase.ScopeDigest, err = coderepair.ScopeDigest(repairCase, binding)
	if err != nil {
		t.Fatal(err)
	}
	event := func(kind string) coderepair.Event {
		return coderepair.Event{ID: uuid.NewString(), CaseID: repairCase.ID, Type: kind, ActorID: "fixture-operator", DetailsJSON: `{}`, CreatedAt: now}
	}
	if err := store.CreateRepairProposal(ctx, repairCase, event("case_proposed"), snapshot); err != nil {
		t.Fatal(err)
	}
	attempt := coderepair.Attempt{ID: uuid.NewString(), CaseID: repairCase.ID, Number: 1, Status: coderepair.JobQueued,
		Provider: selection.Provider, Model: selection.Model, PromptVersion: selection.PromptVersion, Runtime: selection.Runtime, CreatedAt: now}
	attempt.Runtime.ThreadID = repairCase.ID + ":" + attempt.ID
	payload, _ := json.Marshal(map[string]any{"case_id": repairCase.ID, "attempt_id": attempt.ID, "expected_version": int64(2)})
	job := coderepair.Job{ID: uuid.NewString(), CaseID: repairCase.ID, AttemptID: attempt.ID, Type: coderepair.JobTypeInvestigation,
		DedupKey: coderepair.InvestigationDedupKey(repairCase.ID, 1), PayloadJSON: string(payload), Status: coderepair.JobQueued,
		MaxAttempts: 1, AvailableAt: now, CreatedAt: now}
	approval := coderepair.Approval{ID: uuid.NewString(), CaseID: repairCase.ID, CaseVersion: 1, Phase: "investigation", ActorID: "fixture-operator",
		Decision: "approved", ScopeDigest: repairCase.ScopeDigest, PolicyVersion: repairCase.PolicyVersion, CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute)}
	if ok, err := store.ApproveRepairInvestigation(ctx, approval, attempt, job, event("investigation_approved")); err != nil || !ok {
		t.Fatalf("native approval: ok=%v err=%v", ok, err)
	}
	job, err = store.ClaimRepairJob(ctx, "native-integration", now, 8*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	repairCase, err = store.GetRepairCase(ctx, repairCase.ID)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err = store.GetRepairAttempt(ctx, attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	return job, repairCase, attempt, snapshot
}
