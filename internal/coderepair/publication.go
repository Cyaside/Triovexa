package coderepair

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type Publication struct {
	ID          string
	CaseID      string
	AttemptID   string
	ApprovalID  string
	OperationID string
	BranchName  string
	PatchSHA256 string
	HeadSHA     string
	MergeSHA    string
	PRNumber    int64
	PRURL       string
	State       string
	LeaseOwner  string
	LeaseToken  string
	LeaseUntil  time.Time
	Attempts    int
	Failures    int
	LastError   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

const (
	PublicationQueued  = "queued"
	PublicationRunning = "running"
	PublicationPROpen  = "pr_open"
	PublicationBlocked = "blocked"
	PublicationMerged  = "merged"
	PublicationClosed  = "closed_without_merge"
)

func PublicationBranch(caseID string, attemptNumber int) (string, error) {
	if !safeEvidenceIdentifier(caseID) || attemptNumber < 1 {
		return "", errors.New("publication requires a valid case and attempt")
	}
	return fmt.Sprintf("triovexa/repair/%s/%d", caseID, attemptNumber), nil
}

// PublicationDigest binds the reviewed patch to the exact repository scope,
// policy and attempt. A later patch or binding change invalidates approval.
func PublicationDigest(c Case, attempt Attempt, patchSHA256 string) (string, error) {
	if c.ID == "" || attempt.CaseID != c.ID || attempt.ID == "" ||
		!ValidGitRevision(c.BaseSHA) || len(patchSHA256) != 64 ||
		c.ScopeDigest == "" || c.PolicyVersion == "" {
		return "", errors.New("publication scope is incomplete")
	}
	if _, err := hex.DecodeString(patchSHA256); err != nil {
		return "", errors.New("publication patch digest is invalid")
	}
	encoded, err := json.Marshal(struct {
		CaseID, IncidentID, BindingID, AttemptID, BaseSHA, ScopeDigest, PolicyVersion, PatchSHA256 string
	}{c.ID, c.IncidentID, c.BindingID, attempt.ID, c.BaseSHA, c.ScopeDigest, c.PolicyVersion, patchSHA256})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
