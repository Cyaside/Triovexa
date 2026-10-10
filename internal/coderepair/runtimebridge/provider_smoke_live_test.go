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
// live-model repair on a synthetic repository or an explicitly configured
// isolated test branch. It never publishes a PR or changes production services.
func TestNativeProviderSmoke(t *testing.T) {
	mode := os.Getenv("RUN_NATIVE_PROVIDER_SMOKE")
	reviewed := mode == "one-approved-case-after-reviewed-context-fix"
	providerManaged := mode == "provider-managed-on-isolated-repository"
	repositorySmoke := mode == "last-repair-slot-on-isolated-repository" || providerManaged
	if _, ci := os.LookupEnv("CI"); ci || (mode != "one-approved-case" && !reviewed && !repositorySmoke) {
		t.Fatal("live provider validation requires explicit local opt-in and is forbidden in CI")
	}
	if !reviewed && (os.Getenv("LIVE_ACKNOWLEDGED_FAILURE_PATH") != "" || os.Getenv("LIVE_ACKNOWLEDGED_ATTEMPT_ID") != "") {
		t.Fatal("reviewed attempt acknowledgment requires its explicit operator opt-in")
	}
	if !repositorySmoke && (os.Getenv("LIVE_ACKNOWLEDGED_FAILURE_PATHS") != "" || os.Getenv("LIVE_REPOSITORY_CONFIG_PATH") != "") {
		t.Fatal("external repository validation requires its explicit local opt-in")
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
	if !providerManaged {
		if err := validateProviderSmokeBudget(budget, budget.Campaign, 0); err != nil {
			t.Fatal(err)
		}
	} else if !budget.Campaign.ProviderManaged || budget.Campaign.Profile != "internal" || budget.Campaign.ID == "triovexa-cr56-final-smoke-v1" {
		t.Fatal("provider-managed validation requires a distinct explicitly configured internal campaign")
	}
	if providerManaged && os.Getenv("LIVE_ACKNOWLEDGED_FAILURE_PATHS") != "" {
		t.Fatal("provider-managed validation must not borrow the previous campaign allocation")
	}
	if err := budget.Validate(); err != nil {
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
		"profile": budget.Campaign.Profile, "provider_managed": providerManaged, "approval_source": "synthetic-local-fixture", "real_github_writes": 0, "real_deployments": 0,
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
	var ledger *admission.Service
	var before admission.Campaign
	var prior int64
	if !providerManaged {
		ledger, _ = admission.NewService(aibudget.New(shared))
		before, err = ledger.GetCampaign(ctx, budget.Campaign.ID)
		if err != nil {
			t.Fatal("the configured campaign must already exist in the shared ledger")
		}
		if shared.QueryRowContext(ctx, `SELECT count(*) FROM ai_model_requests WHERE campaign_id=$1 AND identity_json->>'phase'='repair'`, before.ID).Scan(&prior) != nil {
			t.Fatal("previous repair dispatches could not be audited")
		}
	}
	if providerManaged {
		proof["authorization_mode"], proof["approval_source"] = mode, "operator-authorized-provider-limit"
		proof["ledger_scope"] = "retained-isolated-test-schema"
	} else if repositorySmoke {
		paths := filepath.SplitList(os.Getenv("LIVE_ACKNOWLEDGED_FAILURE_PATHS"))
		var reports []reviewedProviderSmokeAttempt
		var summaries []modelgateway.Summary
		for _, path := range paths {
			report := readRepositorySmokeFailure(t, repo, path, output)
			summary, err := modelgateway.ReadSummary(ctx, ledger, budget.Campaign.ID, report.AttemptID, "repair", 4)
			if err != nil {
				t.Fatal("retained ledger receipt is unavailable")
			}
			reports, summaries = append(reports, report), append(summaries, summary)
		}
		if err := validateRemainingProviderSmoke(budget, before, prior, reports, summaries); err != nil {
			t.Fatal("the final repair slot does not match both retained failures and original allocation")
		}
		proof["authorization_mode"], proof["approval_source"] = mode, "operator-authorized-isolated-repository"
	} else if reviewed {
		previousPath := os.Getenv("LIVE_ACKNOWLEDGED_FAILURE_PATH")
		acknowledgedAttempt := os.Getenv("LIVE_ACKNOWLEDGED_ATTEMPT_ID")
		if previousPath == output || !filepath.IsAbs(previousPath) || !strings.HasSuffix(previousPath, ".json") {
			t.Fatal("a retained private failure report and distinct new output are required")
		}
		resolved, err := filepath.EvalSymlinks(previousPath)
		if err != nil {
			t.Fatal("acknowledged failure report is unavailable")
		}
		relative, err := filepath.Rel(filepath.Join(repo, "artifacts"), resolved)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
			t.Fatal("acknowledged failure must remain in private artifacts")
		}
		previous, err := os.Open(resolved)
		if err != nil {
			t.Fatal("acknowledged failure report is unavailable")
		}
		raw, readErr := io.ReadAll(io.LimitReader(previous, 65537))
		_ = previous.Close()
		var report reviewedProviderSmokeAttempt
		if readErr != nil || len(raw) > 65536 || json.Unmarshal(raw, &report) != nil {
			t.Fatal("acknowledged failure report is invalid")
		}
		retained, err := modelgateway.ReadSummary(ctx, ledger, budget.Campaign.ID, acknowledgedAttempt, "repair", 4)
		if err != nil || validateReviewedProviderSmokeAttempt(budget, before, prior, acknowledgedAttempt, report, retained) != nil {
			t.Fatal("reviewed attempt did not match the retained failure and original campaign")
		}
		proof["acknowledged_attempt_id"] = acknowledgedAttempt
		proof["authorization_mode"] = mode
	} else if err := validateProviderSmokeBudget(budget, before, prior); err != nil {
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
	if providerManaged {
		// New explicitly authorized test mode; the original shared campaign and
		// its exhausted allocation remain untouched in the application database.
		ledger, _ = admission.NewService(f.store.ModelBudgetStore())
		if schema := os.Getenv("LIVE_PROVIDER_ACCOUNTING_SCHEMA"); schema != "" {
			ledger = retainedProviderSmokeLedger(t, isolated, schema, budget.Campaign.ID)
			proof["retained_accounting_schema"] = schema
		} else {
			proof["retained_accounting_schema"] = f.appSchema
		}
		if err := ledger.CreateCampaign(ctx, budget.Campaign); err != nil {
			t.Fatal("provider-managed accounting could not be initialized")
		}
		before, err = ledger.GetCampaign(ctx, budget.Campaign.ID)
		if err != nil {
			t.Fatal("provider-managed accounting is unavailable")
		}
		proof["campaign_before"] = before
	}
	checkout := strings.TrimSuffix(output, ".json") + "-repository"
	proof["retained_checkout"] = checkout
	if err := os.Mkdir(checkout, 0700); err != nil {
		t.Fatal("a new private retained fixture checkout is required")
	}
	selection, err := SealSelection(client, budget, cipher)
	if err != nil {
		t.Fatal("saved provider selection could not be sealed")
	}
	prepared := prepareProviderSmokeFixture(t, ctx, f, checkout, selection, repositorySmoke, proof)
	w, binding, base := prepared.workspace, prepared.binding, prepared.base
	job, repairCase, attempt, snapshot := prepared.job, prepared.repairCase, prepared.attempt, prepared.snapshot
	proof["case_id"], proof["attempt_id"], proof["base_sha"] = repairCase.ID, attempt.ID, base
	manifest("prepared")
	tests := &persistedDockerTester{image: f.image}
	// The retained failed request consumed part of the same four-decision
	// repair allocation. An explicitly reviewed attempt cannot replenish it.
	repairRequestsRemaining := DefaultLimits().MaxModelRequests - int(prior)
	if providerManaged {
		repairRequestsRemaining, err = nativeModelRequestLimit("internal", 0)
		if err != nil {
			t.Fatal(err)
		}
	}
	proof["repair_requests_remaining"] = repairRequestsRemaining
	runner := &NativeRunner{Process: Process{Executable: f.node, Entry: f.entry, Version: EngineVersion}, Tests: tests, Store: f.store,
		Ledger: ledger, Cipher: cipher, CheckpointDSN: f.checkpointDSN, CheckpointSchema: f.checkpoint, MaxModelRequests: repairRequestsRemaining}
	claimed := agent.ClaimedInvestigation{Job: job, Case: repairCase, Attempt: attempt, ExpectedVersion: repairCase.Version}
	started := time.Now()
	result := runner.Investigate(agent.WithClaimedInvestigation(ctx, claimed), w, binding, snapshot, selection, prepared.recipe)
	proof["status"] = "failed"
	proof["wall_time_ms"], proof["investigation_status"], proof["code"], proof["runtime"] = time.Since(started).Milliseconds(), result.Status, result.Code, result.Runtime
	proof["hypothesis"], proof["evidence_ids"], proof["patch_sha256"], proof["docker_tests"] = security.Redact(result.Hypothesis), result.EvidenceIDs, result.PatchReport.SHA256, tests.calls
	proof["baseline_test_run"] = result.Before.RecipeID != ""
	proof["candidate_test_run"] = result.After.RecipeID != ""
	proof["baseline_exit_code"] = recordedTestExit(result.Before.RecipeID, result.Before.ExitCode)
	proof["candidate_exit_code"] = recordedTestExit(result.After.RecipeID, result.After.ExitCode)
	auditCtx, auditCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer auditCancel()
	outcome, err := result.Outcome(job, repairCase.Version, prepared.recipe, time.Now().UTC())
	if err != nil {
		t.Fatal("live outcome could not be constructed")
	}
	if ok, err := f.store.RecordRepairInvestigationOutcome(auditCtx, outcome); err != nil || !ok {
		t.Fatal("live outcome could not be durably recorded")
	}
	if ok, err := f.store.CompleteRepairJob(auditCtx, job.ID, job.LeaseToken, time.Now().UTC()); err != nil || !ok {
		t.Fatal("live job completion could not be durably recorded")
	}
	after, err := ledger.GetCampaign(auditCtx, budget.Campaign.ID)
	if err != nil {
		t.Fatal("live campaign accounting is unavailable")
	}
	proof["campaign_after"] = after
	manifest("outcome-recorded")
	if result.Status != coderepair.StatePatchReady || result.Before.ExitCode == 0 || result.After.ExitCode != 0 || result.Runtime == nil ||
		result.Before.RecipeID == "" || result.After.RecipeID == "" ||
		result.Runtime.Accounting.UsageStatus != "known" || result.Runtime.Accounting.ModelRequests < 1 || tests.calls < 2 || tests.calls > 8 ||
		after.Blocked || after.ReservedMicroUSD != 0 || (!providerManaged && after.SpentMicroUSD > 100000) {
		t.Fatal("live validation did not produce a compatible, accounted red-to-green patch; no retry")
	}
	proof["status"] = "passed"
	manifest("completed")
}
