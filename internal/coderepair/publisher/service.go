package publisher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/coderepair/sandbox"
)

type Input struct {
	Case       coderepair.Case
	Attempt    coderepair.Attempt
	Binding    coderepair.RepositoryBinding
	Approval   coderepair.Approval
	Patch      []byte
	ReportJSON []byte
}

type Checkout func(context.Context, coderepair.RepositoryBinding, string) (string, func() error, error)

type Service struct {
	github     *GitHub
	checkout   Checkout
	allowWrite func(context.Context) error
}

func NewService(github *GitHub, checkout Checkout, allowWrite func(context.Context) error) (*Service, error) {
	if github == nil || checkout == nil || allowWrite == nil {
		return nil, errors.New("publisher requires GitHub, an isolated checkout, and a write policy")
	}
	return &Service{github: github, checkout: checkout, allowWrite: allowWrite}, nil
}

func (s *Service) Publish(ctx context.Context, p coderepair.Publication, in Input, now time.Time) (PullRequest, string, error) {
	if ctx == nil || now.IsZero() || p.ID == "" || p.LeaseToken == "" || p.State != coderepair.PublicationRunning ||
		!p.LeaseUntil.After(now) || in.Case.State != coderepair.StatePublishing ||
		p.CaseID != in.Case.ID || p.AttemptID != in.Attempt.ID || p.ApprovalID != in.Approval.ID ||
		p.OperationID == "" || (in.Approval.Phase != "publication" && in.Approval.Phase != "automatic_publication") || in.Approval.Decision != "approved" ||
		in.Approval.CaseID != in.Case.ID || in.Approval.CaseVersion+1 != in.Case.Version ||
		in.Approval.PolicyVersion != in.Case.PolicyVersion || in.Attempt.Status != "succeeded" {
		return PullRequest{}, "", errors.New("publication no longer matches an approved repair case")
	}
	branch, err := coderepair.PublicationBranch(in.Case.ID, in.Attempt.Number)
	if err != nil || p.BranchName != branch {
		return PullRequest{}, "", errors.New("publication branch is not bound to the attempt")
	}
	if err := in.Binding.Validate(); err != nil {
		return PullRequest{}, "", err
	}
	scopeDigest, err := coderepair.ScopeDigest(in.Case, in.Binding)
	if err != nil || !in.Binding.Enabled || scopeDigest != in.Case.ScopeDigest || in.Binding.ID != in.Case.BindingID {
		return PullRequest{}, "", errors.New("repository binding changed before publication")
	}
	digest := sha256.Sum256(in.Patch)
	patchSHA := hex.EncodeToString(digest[:])
	if len(in.Patch) == 0 || len(in.Patch) > 64*1024 || patchSHA != p.PatchSHA256 {
		return PullRequest{}, "", errors.New("publication patch content changed")
	}
	reviewDigest, err := coderepair.PublicationDigest(in.Case, in.Attempt, patchSHA)
	if err != nil || reviewDigest != in.Approval.ScopeDigest {
		return PullRequest{}, "", errors.New("publication approval does not bind the current patch")
	}
	var report struct {
		Status      coderepair.State `json:"status"`
		RecipeID    string           `json:"recipe_id"`
		PatchSHA256 string           `json:"patch_sha256"`
		BeforeExit  int              `json:"before_exit"`
		AfterExit   int              `json:"after_exit"`
		EvidenceIDs []string         `json:"evidence_ids"`
	}
	if err := json.Unmarshal(in.ReportJSON, &report); err != nil || report.Status != coderepair.StatePatchReady ||
		report.PatchSHA256 != patchSHA || report.RecipeID == "" || report.BeforeExit == 0 || report.AfterExit != 0 || len(report.EvidenceIDs) == 0 {
		return PullRequest{}, "", errors.New("publication report lacks matching test proof")
	}
	checkoutCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	path, cleanup, err := s.checkout(checkoutCtx, in.Binding, in.Case.BaseSHA)
	cancel()
	if err != nil {
		return PullRequest{}, "", errors.New("approved base revision could not be checked out")
	}
	if cleanup == nil {
		return PullRequest{}, "", errors.New("publication checkout has no cleanup")
	}
	defer cleanup()
	workspace, err := sandbox.Open(path, in.Binding, sandbox.Limits{
		MaxFileBytes: 256 * 1024, MaxTotalBytes: 512 * 1024, MaxListedFiles: 40, MaxSearchHits: 20,
	})
	if err != nil {
		return PullRequest{}, "", err
	}
	defer workspace.Close()
	patchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	patchReport, err := workspace.ApplyPatch(patchCtx, in.Patch, sandbox.PatchLimits{
		MaxPatchBytes: 64 * 1024, MaxFiles: 5, MaxChangedLines: 300,
	})
	cancel()
	if err != nil || patchReport.SHA256 != patchSHA {
		return PullRequest{}, "", errors.New("approved patch no longer applies within policy")
	}
	files := make([]changedFile, 0, len(patchReport.Files))
	for _, name := range patchReport.Files {
		content, err := workspace.ReadFile(name)
		if err != nil {
			return PullRequest{}, "", err
		}
		files = append(files, changedFile{Path: name, Content: string(content)})
	}
	// A crash after the GitHub write can be reconciled after approval expiry,
	// but only when the remote diff still equals the exact approved patch.
	if pr, head, found, err := s.github.Existing(ctx, in.Binding, p, in.Case.BaseSHA, files); err != nil {
		return PullRequest{}, "", err
	} else if found {
		return pr, head, nil
	}
	if !in.Approval.ExpiresAt.After(now) || in.Approval.CreatedAt.After(now) ||
		in.Approval.ExpiresAt.Sub(in.Approval.CreatedAt) > 15*time.Minute {
		return PullRequest{}, "", errors.New("publication approval expired before GitHub write")
	}
	if in.Approval.Phase == "automatic_publication" {
		grant := in.Binding.Automation
		if grant == nil || !grant.Enabled || !grant.PublishDraftPR || grant.Validate() != nil ||
			grant.AuthorizedBy != in.Approval.ActorID || !grant.ExpiresAt.After(time.Now().UTC()) {
			return PullRequest{}, "", errors.New("automatic draft PR grant changed or expired before GitHub write")
		}
	}
	if err := s.allowWrite(ctx); err != nil {
		return PullRequest{}, "", err
	}
	title := "Repair incident " + in.Case.IncidentID
	body := fmt.Sprintf("## Incident\n\n%s\n\n## Verified change\n\nPatch SHA-256: `%s`\nRegression recipe: `%s` (failed before, passed after).\nEvidence references: %d.\n\nOperation: `%s`\n\nCI and human review are required before merge. Deployment recovery is verified separately.\n",
		in.Case.IncidentID, patchSHA, report.RecipeID, len(report.EvidenceIDs), p.OperationID)
	return s.github.Ensure(ctx, in.Binding, p, in.Case.BaseSHA, files, title, body)
}
