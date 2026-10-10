package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
)

func automaticPublicationFixture(t *testing.T, store *PostgresStore, publish bool) (coderepair.Case, coderepair.Job) {
	t.Helper()
	ctx := context.Background()
	d, b := automaticFixture(t, store)
	b.Automation.PublishDraftPR = publish
	policy, _ := json.Marshal(b.Automation)
	if _, err := store.db.Exec(`UPDATE repository_bindings SET automation_json=$1 WHERE id=$2`, string(policy), b.ID); err != nil {
		t.Fatal(err)
	}
	i := seedAutomaticAlert(t, store, b)
	if handled, err := d.Try(ctx, i); err != nil || !handled {
		t.Fatalf("alert investigation: %v %v", handled, err)
	}
	job, err := store.ClaimRepairJob(ctx, "automatic-fixture", time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	c, err := store.GetRepairCase(ctx, job.CaseID)
	if err != nil {
		t.Fatal(err)
	}
	patch := []byte("diff --git a/src/parser.py b/src/parser.py\nfixture patch\n")
	digest := sha256.Sum256(patch)
	patchSHA := hex.EncodeToString(digest[:])
	report, _ := json.Marshal(map[string]any{"status": coderepair.StatePatchReady, "recipe_id": "python-tests",
		"patch_sha256": patchSHA, "before_exit": 1, "after_exit": 0, "evidence_ids": []string{"log-1"}})
	if ok, err := store.RecordRepairInvestigationOutcome(ctx, coderepair.InvestigationOutcome{JobID: job.ID, LeaseToken: job.LeaseToken,
		CaseID: c.ID, AttemptID: job.AttemptID, ExpectedVersion: c.Version, State: coderepair.StatePatchReady,
		Patch: patch, PatchSHA256: patchSHA, ReportJSON: report, RecordedAt: time.Now().UTC()}); err != nil || !ok {
		t.Fatalf("verified outcome: %v %v", ok, err)
	}
	return c, job
}

func TestAutomaticPublicationCompletesAtomicallyAndRecoversIntegration(t *testing.T) {
	for _, recoverAfterCrash := range []bool{false, true} {
		t.Run(map[bool]string{false: "concurrent-completion", true: "outcome-before-job-crash"}[recoverAfterCrash], func(t *testing.T) {
			store := isolatedRepairStore(t)
			c, job := automaticPublicationFixture(t, store, true)
			ctx := context.Background()
			if recoverAfterCrash {
				store = reopenRepairStore(t, store)
				if count, err := store.RecoverRepairJobs(ctx, job.LeaseUntil.Add(time.Second)); err != nil || count != 1 {
					t.Fatalf("recovery: %d %v", count, err)
				}
			} else {
				var wg sync.WaitGroup
				for range 2 {
					wg.Add(1)
					go func() {
						defer wg.Done()
						if _, err := store.CompleteRepairJob(ctx, job.ID, job.LeaseToken, time.Now().UTC()); err != nil {
							t.Errorf("completion: %v", err)
						}
					}()
				}
				wg.Wait()
			}
			p, err := store.GetRepairPublication(ctx, c.ID)
			if err != nil || p.State != coderepair.PublicationQueued {
				t.Fatalf("queued publication: %+v %v", p, err)
			}
			input, err := store.GetRepairPublicationInput(ctx, p)
			if err != nil || input.Approval.Phase != "automatic_publication" || input.Approval.ActorID != "auto-admin" ||
				input.Approval.CaseVersion+1 != input.Case.Version || input.Attempt.Status != "succeeded" {
				t.Fatalf("grant-bound publication: %+v %v", input, err)
			}
			if count, err := store.RecoverRepairJobs(ctx, job.LeaseUntil.Add(time.Minute)); err != nil || count != 0 {
				t.Fatalf("replayed recovery: %d %v", count, err)
			}
			var count int
			if err := store.db.QueryRow(`SELECT count(*) FROM repair_publications`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("duplicate publication: %d %v", count, err)
			}
		})
	}
}

func TestAutomaticPublicationRejectsRevokedGrantsIntegration(t *testing.T) {
	for _, scenario := range []string{"manual", "expired", "disabled", "admin-removed", "kill-switch"} {
		t.Run(scenario, func(t *testing.T) {
			store := isolatedRepairStore(t)
			c, job := automaticPublicationFixture(t, store, scenario != "manual")
			queries := map[string]string{
				"expired":       `UPDATE repository_bindings SET automation_json=jsonb_set(automation_json,'{expires_at}',to_jsonb(now()-interval '1 hour'))`,
				"disabled":      `UPDATE repository_bindings SET enabled=false`,
				"admin-removed": `UPDATE users SET role='viewer'`,
				"kill-switch":   `INSERT INTO application_settings(key,value,updated_at) VALUES('safety.kill_switch','true',now())`,
			}
			if query := queries[scenario]; query != "" {
				if _, err := store.db.Exec(query); err != nil {
					t.Fatal(err)
				}
			}
			if ok, err := store.CompleteRepairJob(context.Background(), job.ID, job.LeaseToken, time.Now().UTC()); err != nil || !ok {
				t.Fatalf("retained patch completion: %v %v", ok, err)
			}
			ready, _ := store.GetRepairCase(context.Background(), c.ID)
			var count int
			_ = store.db.QueryRow(`SELECT count(*) FROM repair_publications`).Scan(&count)
			if ready.State != coderepair.StatePatchReady || count != 0 {
				t.Fatalf("revoked grant authorized publication: %s %d", ready.State, count)
			}
		})
	}
}

func TestAutomaticPublicationRollbackAndDispatchFenceIntegration(t *testing.T) {
	store := isolatedRepairStore(t)
	c, job := automaticPublicationFixture(t, store, true)
	if _, err := store.db.Exec(`ALTER TABLE repair_publications ADD CONSTRAINT reject_fixture_publication CHECK(false)`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if ok, err := store.CompleteRepairJob(ctx, job.ID, job.LeaseToken, time.Now().UTC()); err == nil || ok {
		t.Fatal("failed publication enqueue was committed")
	}
	current, _ := store.GetRepairJob(ctx, job.ID)
	if current.Status != coderepair.JobRunning {
		t.Fatal("job completion escaped publication transaction")
	}
	if _, err := store.db.Exec(`ALTER TABLE repair_publications DROP CONSTRAINT reject_fixture_publication`); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.CompleteRepairJob(ctx, job.ID, job.LeaseToken, time.Now().UTC()); err != nil || !ok {
		t.Fatalf("completion: %v %v", ok, err)
	}
	if err := store.CheckRepairPublicationSafety(ctx, c.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE users SET role='viewer' WHERE id='auto-admin'`); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckRepairPublicationSafety(ctx, c.ID, time.Now().UTC()); err == nil {
		t.Fatal("revoked admin reached GitHub write")
	}
}
