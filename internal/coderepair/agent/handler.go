package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/coderepair/sandbox"
)

type CheckoutFactory func(context.Context, coderepair.RepositoryBinding, string) (string, func() error, error)

type Handler struct {
	store    coderepair.InvestigationStore
	engine   InvestigationEngine
	checkout CheckoutFactory
}

func NewHandler(store coderepair.InvestigationStore, engine InvestigationEngine, checkout CheckoutFactory) (*Handler, error) {
	if store == nil || engine == nil || checkout == nil {
		return nil, errors.New("repair handler requires store, coding loop and checkout factory")
	}
	return &Handler{store: store, engine: engine, checkout: checkout}, nil
}

func DefaultCheckoutFactory(parent string) CheckoutFactory {
	return func(ctx context.Context, binding coderepair.RepositoryBinding, sha string) (string, func() error, error) {
		checkout, err := sandbox.Checkout(ctx, parent, binding, sha)
		if err != nil {
			return "", nil, err
		}
		container := filepath.Dir(checkout)
		return checkout, func() error { return os.RemoveAll(container) }, nil
	}
}

// Handle is passed to the durable repair runner. Job payloads contain only
// identity and an expected version; binding and evidence are reloaded from
// storage. A model never chooses the repository or the test executable.
func (h *Handler) Handle(ctx context.Context, job coderepair.Job) error {
	if job.ID == "" || job.LeaseToken == "" || job.Status != coderepair.JobRunning ||
		job.Type != coderepair.JobTypeInvestigation || len(job.PayloadJSON) > 4096 {
		return errors.New("repair investigation job is invalid")
	}
	var payload struct {
		CaseID          string `json:"case_id"`
		AttemptID       string `json:"attempt_id"`
		ExpectedVersion int64  `json:"expected_version"`
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(job.PayloadJSON)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil || payload.CaseID != job.CaseID ||
		payload.AttemptID != job.AttemptID || payload.ExpectedVersion < 1 {
		return errors.New("repair investigation job payload is invalid")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return errors.New("repair investigation job has trailing payload")
	}
	caseRecord, err := h.store.GetRepairCase(ctx, job.CaseID)
	if err != nil {
		return err
	}
	if caseRecord.State != coderepair.StateInvestigating || caseRecord.Version != payload.ExpectedVersion ||
		caseRecord.ID != job.CaseID || !coderepair.ValidGitRevision(caseRecord.BaseSHA) {
		return errors.New("repair case no longer matches the claimed investigation")
	}
	attempt, err := h.store.GetRepairAttempt(ctx, job.AttemptID)
	if err != nil {
		return err
	}
	if attempt.CaseID != caseRecord.ID || attempt.Status != coderepair.JobRunning {
		return errors.New("repair attempt no longer matches the claimed investigation")
	}
	binding, err := h.store.GetRepositoryBinding(ctx, caseRecord.BindingID)
	if err != nil {
		return err
	}
	snapshot, err := h.store.GetRepairEvidenceSnapshot(ctx, caseRecord.ID)
	if err != nil {
		return err
	}
	selection := coderepair.AgentSelection{Provider: attempt.Provider, Model: attempt.Model, PromptVersion: attempt.PromptVersion}
	result := InvestigationResult{Status: coderepair.StateBlocked, Provider: selection.Provider, Model: selection.Model,
		Prompt: selection.PromptVersion}
	now := time.Now().UTC()
	var safetyErr error
	if safety, ok := h.store.(interface {
		CheckRepairInvestigationSafety(context.Context, string, time.Time) error
	}); ok {
		safetyErr = safety.CheckRepairInvestigationSafety(ctx, caseRecord.ID, now)
	}
	if safetyErr != nil {
		result.Code, result.Reason = "SCOPE_REVOKED", "investigation authorization was revoked or expired before checkout"
	} else if !binding.Enabled || binding.ID != caseRecord.BindingID || binding.ServiceName != snapshot.ServiceName ||
		binding.Environment != snapshot.Environment || binding.PolicyVersion != caseRecord.PolicyVersion ||
		snapshot.IncidentID != caseRecord.IncidentID || snapshot.DeployedRevision != caseRecord.DeployedSHA ||
		!snapshot.VerifyDigest() || snapshot.CapturedAt.After(now.Add(5*time.Second)) {
		result.Code, result.Reason = "EVIDENCE_STALE", "approved evidence or repository binding is no longer current"
	} else if digest, err := coderepair.ScopeDigest(caseRecord, binding); err != nil || digest != caseRecord.ScopeDigest {
		result.Code, result.Reason = "SCOPE_CHANGED", "approved repository scope changed before investigation"
	} else if len(binding.TestRecipes) != 1 {
		result.Code, result.Reason = "TEST_UNAVAILABLE", "investigation requires one approved regression recipe"
	} else {
		checkoutCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		path, cleanup, checkoutErr := h.checkout(checkoutCtx, binding, caseRecord.BaseSHA)
		cancel()
		if checkoutErr != nil {
			result.Code, result.Reason = "CHECKOUT_FAILED", "approved base revision could not be checked out"
		} else {
			if cleanup == nil {
				return errors.New("checkout factory did not supply cleanup")
			}
			defer cleanup()
			workspace, openErr := sandbox.Open(path, binding, sandbox.Limits{
				MaxFileBytes: 32 * 1024, MaxTotalBytes: 200 * 1024, MaxListedFiles: 20, MaxSearchHits: 20})
			if openErr != nil {
				result.Code, result.Reason = "CHECKOUT_INVALID", "approved checkout is not a safe workspace"
			} else {
				engineCtx := WithClaimedInvestigation(ctx, ClaimedInvestigation{Job: job, ExpectedVersion: payload.ExpectedVersion,
					Case: caseRecord, Attempt: attempt, RecoveryOnly: now.Sub(snapshot.CapturedAt) > time.Minute})
				result = h.engine.Investigate(engineCtx, workspace, binding, snapshot, selection, binding.TestRecipes[0])
				_ = workspace.Close()
			}
		}
	}
	recipeID := "unavailable"
	if len(binding.TestRecipes) == 1 {
		recipeID = binding.TestRecipes[0]
	}
	outcome, err := result.Outcome(job, payload.ExpectedVersion, recipeID, time.Now().UTC())
	if err != nil {
		return err
	}
	recorded, err := h.store.RecordRepairInvestigationOutcome(ctx, outcome)
	if err != nil {
		return err
	}
	if !recorded {
		return errors.New("repair investigation lease or case version changed before outcome recording")
	}
	return nil
}
