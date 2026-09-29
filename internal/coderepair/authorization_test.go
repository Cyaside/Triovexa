package coderepair

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type authorizationRepository struct {
	caseRecord Case
	snapshot   EvidenceSnapshot
	approved   int
	approval   Approval
	attempt    Attempt
	job        Job
	event      Event
	accept     bool
}

func (r *authorizationRepository) GetRepairCase(context.Context, string) (Case, error) {
	return r.caseRecord, nil
}

func (r *authorizationRepository) GetRepairEvidenceSnapshot(context.Context, string) (EvidenceSnapshot, error) {
	return r.snapshot, nil
}

func (r *authorizationRepository) ApproveRepairInvestigation(_ context.Context, approval Approval, attempt Attempt, job Job, event Event) (bool, error) {
	r.approved++
	r.approval, r.attempt, r.job, r.event = approval, attempt, job, event
	return r.accept, nil
}

func authorizationFixture(t *testing.T, now time.Time) *authorizationRepository {
	t.Helper()
	incident, binding, items := evidenceFixture(now)
	snapshot, err := BuildEvidenceSnapshot(incident, items, now,
		EvidenceLimits{MaxItems: 10, MaxSnippetBytes: 2048, MaxTotalBytes: 4096, MaxAge: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	c := Case{ID: "repair-case-1", IncidentID: incident.ID, BindingID: binding.ID,
		BaseSHA: strings.Repeat("b", 40), DeployedSHA: snapshot.DeployedRevision,
		PolicyVersion: binding.PolicyVersion, State: StateAwaitingInvestigationApproval, Version: 1}
	c.ScopeDigest, err = ScopeDigest(c, binding)
	if err != nil {
		t.Fatal(err)
	}
	return &authorizationRepository{caseRecord: c, snapshot: snapshot, accept: true}
}

func TestInvestigationAuthorizationCreatesBoundedDurableJob(t *testing.T) {
	now := time.Now().UTC()
	repo := authorizationFixture(t, now)
	service, err := NewInvestigationAuthorizationService(repo)
	if err != nil {
		t.Fatal(err)
	}
	selection := AgentSelection{Provider: "openai-compatible", Model: "glm-5.3-flash", PromptVersion: "repair-v1"}
	approval, attempt, job, err := service.Authorize(context.Background(), repo.caseRecord.ID, "operator-1", selection, now)
	if err != nil {
		t.Fatal(err)
	}
	if repo.approved != 1 || approval.CaseVersion != 1 || approval.ScopeDigest != repo.caseRecord.ScopeDigest ||
		approval.ExpiresAt.Sub(approval.CreatedAt) != 15*time.Minute {
		t.Fatalf("approval not bound to case: %+v", approval)
	}
	if attempt.Provider != selection.Provider || attempt.Model != selection.Model ||
		attempt.PromptVersion != selection.PromptVersion || attempt.Status != JobQueued || attempt.Number != 1 {
		t.Fatalf("attempt did not snapshot the configured model: %+v", attempt)
	}
	var payload struct {
		CaseID          string `json:"case_id"`
		AttemptID       string `json:"attempt_id"`
		ExpectedVersion int64  `json:"expected_version"`
	}
	if err := json.Unmarshal([]byte(job.PayloadJSON), &payload); err != nil ||
		payload.CaseID != approval.CaseID || payload.AttemptID != attempt.ID || payload.ExpectedVersion != 2 ||
		job.MaxAttempts != 2 || job.DedupKey != InvestigationDedupKey(approval.CaseID, 1) {
		t.Fatalf("job is not bound to the approved attempt: %+v, %v", job, err)
	}
	if repo.event.Type != "investigation_approved" || !strings.Contains(repo.event.DetailsJSON, repo.snapshot.SHA256) {
		t.Fatalf("approval audit missing evidence digest: %+v", repo.event)
	}
}

func TestInvestigationAuthorizationFailsClosed(t *testing.T) {
	now := time.Now().UTC()
	selection := AgentSelection{Provider: "openai-compatible", Model: "glm-5.3-flash", PromptVersion: "repair-v1"}
	for name, mutate := range map[string]func(*authorizationRepository, *AgentSelection){
		"wrong state": func(r *authorizationRepository, _ *AgentSelection) { r.caseRecord.State = StateInvestigating },
		"altered evidence": func(r *authorizationRepository, _ *AgentSelection) {
			r.snapshot.DeployedRevision = strings.Repeat("f", 40)
		},
		"missing provider": func(_ *authorizationRepository, s *AgentSelection) { s.Provider = "" },
		"version conflict": func(r *authorizationRepository, _ *AgentSelection) { r.accept = false },
	} {
		t.Run(name, func(t *testing.T) {
			repo := authorizationFixture(t, now)
			chosen := selection
			mutate(repo, &chosen)
			service, err := NewInvestigationAuthorizationService(repo)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, _, err := service.Authorize(context.Background(), repo.caseRecord.ID, "operator-1", chosen, now); err == nil {
				t.Fatal("authorization accepted an invalid request or stale case")
			}
			if name != "version conflict" && repo.approved != 0 {
				t.Fatal("repository mutation was attempted after preflight failure")
			}
		})
	}
}
