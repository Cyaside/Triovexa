package coderepair

import (
	"context"
	"errors"
	"time"
)

var ErrNoJobAvailable = errors.New("code repair: no job available")

// CaseStore owns durable case transitions and investigation authorization.
// Implementations must update state and audit in the same transaction.
type CaseStore interface {
	CreateRepositoryBinding(context.Context, RepositoryBinding) error
	GetActiveRepositoryBinding(context.Context, string, string) (RepositoryBinding, error)
	CreateRepairCase(context.Context, Case, Event) error
	CreateRepairProposal(context.Context, Case, Event, EvidenceSnapshot) error
	GetRepairEvidenceSnapshot(context.Context, string) (EvidenceSnapshot, error)
	GetRepairCase(context.Context, string) (Case, error)
	TransitionRepairCase(context.Context, string, State, int64, State, Event) (bool, error)
	ApproveRepairInvestigation(context.Context, Approval, Attempt, Job, Event) (bool, error)
	ListRepairEvents(context.Context, string) ([]Event, error)
}

// ArtifactStore records a manifest and its audit event atomically. Artifact
// bytes remain outside the database and are addressed by digest and reference.
type ArtifactStore interface {
	AddRepairArtifact(context.Context, Artifact, Event) error
}

// JobStore leases repair work independently of the existing triage runner.
// Every mutation after claim requires the current fencing token.
type JobStore interface {
	GetRepairJob(context.Context, string) (Job, error)
	ClaimRepairJob(context.Context, string, time.Time, time.Duration) (Job, error)
	RenewRepairJobLease(context.Context, string, string, time.Time, time.Duration) (bool, error)
	CompleteRepairJob(context.Context, string, string, time.Time) (bool, error)
	FailRepairJob(context.Context, string, string, string, time.Time, time.Time, bool) (bool, error)
	RecoverRepairJobs(context.Context, time.Time) (int, error)
}
