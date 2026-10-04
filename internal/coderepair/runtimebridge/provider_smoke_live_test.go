//go:build provider_smoke

package runtimebridge

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/ai/admission"
	"github.com/Cyaside/Triovexa/internal/ai/modelgateway"
	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/coderepair/agent"
	"github.com/Cyaside/Triovexa/internal/config"
	"github.com/Cyaside/Triovexa/internal/security"
	"github.com/Cyaside/Triovexa/internal/storage/postgres/aibudget"
)

// This opt-in test is excluded from ordinary builds and CI. It performs one
// live-model repair on an isolated local repository, with synthetic approval.
// It never publishes a PR, injects a fault into a running service, or deploys.
func TestNativeProviderSmoke(t *testing.T) {
	if _, ci := os.LookupEnv("CI"); ci || os.Getenv("RUN_NATIVE_PROVIDER_SMOKE") != "one-approved-case" {
		t.Fatal("live provider validation requires explicit local opt-in and is forbidden in CI")
	}
	budgetPath, output := os.Getenv("AI_BUDGET_CONFIG_PATH"), os.Getenv("LIVE_PROVIDER_EVIDENCE_PATH")
	if !filepath.IsAbs(budgetPath) || !filepath.IsAbs(output) || !strings.HasSuffix(output, ".json") {
		t.Fatal("absolute budget and local JSON evidence paths are required")
	}
	repo, _ := filepath.Abs("../../..")
	relative, err := filepath.Rel(filepath.Join(repo, "artifacts"), output)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		t.Fatal("live evidence must remain in the ignored repository artifacts directory")
	}
	budget, err := modelgateway.LoadBudgetConfig(budgetPath)
	if err != nil {
		t.Fatal("live model admission configuration is unavailable")
	}
	if err := validateProviderSmokeBudget(budget, budget.Campaign, 0); err != nil {
		t.Fatal(err)
	}
	if budget.Campaign.ID != os.Getenv("LIVE_EXPECTED_CAMPAIGN_ID") {
		t.Fatal("campaign must match the explicitly selected existing shared campaign")
	}
	if os.Getenv("TEST_DATABASE_URL") == "" || os.Getenv("TEST_AGENT_RUNTIME_ENTRY") == "" || os.Getenv("TEST_REPAIR_SANDBOX_IMAGE") == "" {
		t.Fatal("isolated PostgreSQL, built native runtime, and prebuilt sandbox are required")
	}
	if os.Getenv("LIVE_SHARED_DATABASE_URL") == "" {
		t.Fatal("the existing shared application database must be selected explicitly")
	}
	if os.MkdirAll(filepath.Dir(output), 0700) != nil {
		t.Fatal("local evidence directory is unavailable")
	}
	file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal("evidence path must be new; existing proof must not be overwritten")
	}
	proof := map[string]any{"status": "blocked", "campaign_id": budget.Campaign.ID, "model": budget.Pricing.Model,
		"profile": "final-smoke", "approval_source": "synthetic-local-fixture", "real_github_writes": 0, "real_deployments": 0,
		"provider_invoice_reconciled": false, "created_at": time.Now().UTC()}
	t.Cleanup(func() {
		if err := json.NewEncoder(file).Encode(proof); err != nil {
			t.Error("live proof could not be saved")
		}
		if err := file.Sync(); err != nil {
			t.Error("live proof could not be flushed")
		}
		_ = file.Close()
	})
	journal, err := os.OpenFile(output+".manifest.jsonl", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal("a new private recovery manifest is required")
	}
	t.Cleanup(func() { _ = journal.Close() })
	manifest := func(phase string) {
		proof["phase"] = phase
		if err := json.NewEncoder(journal).Encode(proof); err != nil {
			t.Fatal("recovery manifest could not be saved before effects")
		}
		if err := journal.Sync(); err != nil {
			t.Fatal("recovery manifest could not be flushed before effects")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
	defer cancel()
	// Open the existing application pool without running migrations or creating
	// a campaign. This same ledger accounts connection/triage/remediation calls.
	shared, err := sql.Open("pgx", os.Getenv("LIVE_SHARED_DATABASE_URL"))
	if err != nil {
		t.Fatal("shared ledger connection is unavailable")
	}
	defer shared.Close()
	isolated, err := openSmokeTestDatabase(os.Getenv("TEST_DATABASE_URL"), os.Getenv("LIVE_ISOLATED_DATABASE_NAME"))
	if err != nil {
		t.Fatal(err)
	}
	defer isolated.Close()
	if err := validateSmokeDatabaseIsolation(ctx, shared, isolated, os.Getenv("LIVE_ISOLATED_DATABASE_NAME")); err != nil {
		t.Fatal(err)
	}
	proof["isolated_database_verified"] = true
	lock, err := shared.Conn(ctx)
	if err != nil {
		t.Fatal("shared ledger connection is unavailable")
	}
	defer lock.Close()
	var acquired bool
	if lock.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(1433770070, hashtext($1))`, budget.Campaign.ID).Scan(&acquired) != nil || !acquired {
		t.Fatal("another provider validation owns this campaign")
	}
	defer func() {
		unlockCtx, unlockCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer unlockCancel()
		_, _ = lock.ExecContext(unlockCtx, `SELECT pg_advisory_unlock(1433770070, hashtext($1))`, budget.Campaign.ID)
	}()
	ledger, _ := admission.NewService(aibudget.New(shared))
	before, err := ledger.GetCampaign(ctx, budget.Campaign.ID)
	if err != nil {
		t.Fatal("the configured campaign must already exist in the shared ledger")
	}
	var prior int64
	if shared.QueryRowContext(ctx, `SELECT count(*) FROM ai_model_requests WHERE campaign_id=$1 AND identity_json->>'phase'='repair'`, before.ID).Scan(&prior) != nil {
		t.Fatal("previous repair dispatches could not be audited")
	}
	if err := validateProviderSmokeBudget(budget, before, prior); err != nil {
		t.Fatal(err)
	}
	proof["campaign_before"] = before
	var saved string
	if shared.QueryRowContext(ctx, `SELECT value FROM application_settings WHERE key=$1`, config.ReasoningConnectionBundleSettingKey).Scan(&saved) != nil {
		t.Fatal("a saved encrypted UI connection is required; environment selection is disabled")
	}
	client, cipher, err := smokeSavedProvider(ctx, saved, budget.Pricing, os.Getenv("LIVE_PROVIDER_ALLOWED_HOST"), func() (string, error) {
		path := os.Getenv("LIVE_CREDENTIAL_KEY_PATH")
		if !filepath.IsAbs(path) {
			return "", os.ErrNotExist
		}
		key, err := os.Open(path)
		if err != nil {
			return "", err
		}
		defer key.Close()
		raw, err := io.ReadAll(io.LimitReader(key, 129))
		if err != nil || len(raw) > 128 {
			return "", os.ErrInvalid
		}
		return string(raw), nil // Existing key only; NewCipher never creates a file.
	})
	if err != nil {
		t.Fatal(err)
	}
	// Retain isolated domain/checkpoint schemas and encrypted proof on success
	// or failure. Never drop the shared ledger or issue another paid attempt.
	f := persistedNativeFixtureRetained(t, true, func(appSchema, checkpoint, role string) {
		proof["allocated_application_schema"], proof["allocated_checkpoint_schema"], proof["allocated_checkpoint_role"] = appSchema, checkpoint, role
		manifest("allocated")
	})
	proof["checkpoint_initialization_complete"] = true
	checkout := strings.TrimSuffix(output, ".json") + "-repository"
	proof["retained_checkout"] = checkout
	if err := os.Mkdir(checkout, 0700); err != nil {
		t.Fatal("a new private retained fixture checkout is required")
	}
	w, binding, base := compatibilityFixtureAt(t, checkout)
	selection, err := SealSelection(client, budget, cipher)
	if err != nil {
		t.Fatal("saved provider selection could not be sealed")
	}
	job, repairCase, attempt, snapshot := approveNativeFixture(t, f.store, binding, base, strings.Repeat("a", 40), selection)
	proof["case_id"], proof["attempt_id"], proof["base_sha"] = repairCase.ID, attempt.ID, base
	manifest("prepared")
	tests := &persistedDockerTester{image: f.image}
	runner := &NativeRunner{Process: Process{Executable: f.node, Entry: f.entry, Version: EngineVersion}, Tests: tests, Store: f.store,
		Ledger: ledger, Cipher: cipher, CheckpointDSN: f.checkpointDSN, CheckpointSchema: f.checkpoint}
	claimed := agent.ClaimedInvestigation{Job: job, Case: repairCase, Attempt: attempt, ExpectedVersion: repairCase.Version}
	started := time.Now()
	result := runner.Investigate(agent.WithClaimedInvestigation(ctx, claimed), w, binding, snapshot, selection, "go-test-workload")
	proof["status"] = "failed"
	proof["wall_time_ms"], proof["investigation_status"], proof["code"], proof["runtime"] = time.Since(started).Milliseconds(), result.Status, result.Code, result.Runtime
	proof["hypothesis"], proof["evidence_ids"], proof["patch_sha256"], proof["docker_tests"] = security.Redact(result.Hypothesis), result.EvidenceIDs, result.PatchReport.SHA256, tests.calls
	proof["baseline_exit_code"], proof["candidate_exit_code"] = result.Before.ExitCode, result.After.ExitCode
	auditCtx, auditCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer auditCancel()
	outcome, err := result.Outcome(job, repairCase.Version, "go-test-workload", time.Now().UTC())
	if err != nil {
		t.Fatal("live outcome could not be constructed")
	}
	if ok, err := f.store.RecordRepairInvestigationOutcome(auditCtx, outcome); err != nil || !ok {
		t.Fatal("live outcome could not be durably recorded")
	}
	after, err := ledger.GetCampaign(auditCtx, budget.Campaign.ID)
	if err != nil {
		t.Fatal("live campaign accounting is unavailable")
	}
	proof["campaign_after"] = after
	if result.Status != coderepair.StatePatchReady || result.Before.ExitCode == 0 || result.After.ExitCode != 0 || result.Runtime == nil ||
		result.Runtime.Accounting.UsageStatus != "known" || result.Runtime.Accounting.ModelRequests < 1 || tests.calls < 2 || tests.calls > 8 ||
		after.Blocked || after.ReservedMicroUSD != 0 || after.SpentMicroUSD > 100000 {
		t.Fatal("live validation did not produce a compatible, accounted red-to-green patch inside the target budget; no retry")
	}
	proof["status"] = "passed"
	manifest("completed")
}
