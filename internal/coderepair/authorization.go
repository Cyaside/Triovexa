package coderepair

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/Cyaside/Triovexa/internal/security"
)

type InvestigationApprovalRepository interface {
	GetRepairCase(context.Context, string) (Case, error)
	GetRepairEvidenceSnapshot(context.Context, string) (EvidenceSnapshot, error)
	ApproveRepairInvestigation(context.Context, Approval, Attempt, Job, Event) (bool, error)
}

// AgentSelection is resolved from server configuration, not incident input.
// It is recorded on the attempt so later provider changes do not rewrite it.
type AgentSelection struct {
	Provider      string
	Model         string
	PromptVersion string
	Runtime       *RuntimeSpec
}

func (s AgentSelection) validate() error {
	if s.Runtime != nil {
		if err := s.Runtime.Validate(); err != nil {
			return err
		}
		if s.Runtime.ThreadID != "" {
			return errors.New("thread identity must be assigned by authorization")
		}
	}
	for _, value := range []string{s.Provider, s.Model, s.PromptVersion} {
		if value == "" || strings.TrimSpace(value) != value || len(value) > 128 ||
			strings.IndexFunc(value, unicode.IsControl) >= 0 || security.Redact(value) != value {
			return errors.New("investigation provider, model and prompt version must be configured")
		}
	}
	return nil
}

type InvestigationAuthorizationService struct {
	repository InvestigationApprovalRepository
}

func NewInvestigationAuthorizationService(repository InvestigationApprovalRepository) (*InvestigationAuthorizationService, error) {
	if repository == nil {
		return nil, errors.New("investigation authorization requires a repository")
	}
	return &InvestigationAuthorizationService{repository: repository}, nil
}

// Authorize creates one bounded investigation attempt. The repository applies
// the approval, case transition, audit and job in a single transaction.
func (s *InvestigationAuthorizationService) Authorize(ctx context.Context, caseID, actorID string, selection AgentSelection, now time.Time) (Approval, Attempt, Job, error) {
	if !safeEvidenceIdentifier(caseID) || !safeEvidenceIdentifier(actorID) || now.IsZero() {
		return Approval{}, Attempt{}, Job{}, errors.New("investigation authorization requires case, actor and time")
	}
	if err := selection.validate(); err != nil {
		return Approval{}, Attempt{}, Job{}, err
	}
	c, err := s.repository.GetRepairCase(ctx, caseID)
	if err != nil {
		return Approval{}, Attempt{}, Job{}, err
	}
	if c.State != StateAwaitingInvestigationApproval || c.Version < 1 || c.ScopeDigest == "" {
		return Approval{}, Attempt{}, Job{}, errors.New("repair case is not awaiting investigation approval")
	}
	snapshot, err := s.repository.GetRepairEvidenceSnapshot(ctx, caseID)
	if err != nil {
		return Approval{}, Attempt{}, Job{}, err
	}
	if !snapshot.VerifyDigest() || snapshot.IncidentID != c.IncidentID || snapshot.DeployedRevision != c.DeployedSHA {
		return Approval{}, Attempt{}, Job{}, errors.New("repair evidence no longer matches the proposed case")
	}
	now = now.UTC()
	attempt := Attempt{ID: uuid.NewString(), CaseID: c.ID, Number: 1, Status: JobQueued,
		Provider: selection.Provider, Model: selection.Model, PromptVersion: selection.PromptVersion, CreatedAt: now}
	if selection.Runtime != nil {
		runtime := *selection.Runtime
		runtime.ThreadID = c.ID + ":" + attempt.ID
		attempt.Runtime = &runtime
	}
	payload, err := json.Marshal(struct {
		CaseID          string `json:"case_id"`
		AttemptID       string `json:"attempt_id"`
		ExpectedVersion int64  `json:"expected_version"`
	}{c.ID, attempt.ID, c.Version + 1})
	if err != nil {
		return Approval{}, Attempt{}, Job{}, err
	}
	job := Job{ID: uuid.NewString(), CaseID: c.ID, AttemptID: attempt.ID,
		Type: JobTypeInvestigation, DedupKey: InvestigationDedupKey(c.ID, attempt.Number),
		PayloadJSON: string(payload), Status: JobQueued, MaxAttempts: 2,
		AvailableAt: now, CreatedAt: now, UpdatedAt: now}
	approval := Approval{ID: uuid.NewString(), CaseID: c.ID, CaseVersion: c.Version,
		Phase: "investigation", ActorID: actorID, Decision: "approved",
		ScopeDigest: c.ScopeDigest, PolicyVersion: c.PolicyVersion,
		ExpiresAt: now.Add(15 * time.Minute), CreatedAt: now}
	eventDetails, err := json.Marshal(map[string]string{
		"evidence_sha256": snapshot.SHA256, "attempt_id": attempt.ID,
		"provider": selection.Provider, "model": selection.Model,
	})
	if err != nil {
		return Approval{}, Attempt{}, Job{}, err
	}
	event := Event{ID: uuid.NewString(), CaseID: c.ID, ActorID: actorID,
		Type: "investigation_approved", DetailsJSON: string(eventDetails), CreatedAt: now}
	approved, err := s.repository.ApproveRepairInvestigation(ctx, approval, attempt, job, event)
	if err != nil {
		return Approval{}, Attempt{}, Job{}, err
	}
	if !approved {
		return Approval{}, Attempt{}, Job{}, errors.New("repair case changed before investigation approval")
	}
	return approval, attempt, job, nil
}
