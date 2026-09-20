package storage

import (
	"context"
	"errors"
	"time"

	"github.com/Cyaside/Triovexa/internal/domain"
)

var ErrNoJobAvailable = errors.New("storage: no job available")

type DurableJobStore interface {
	EnqueueJob(context.Context, domain.WorkflowJob) error
	ClaimJob(context.Context, string, time.Time) (domain.WorkflowJob, error)
	CompleteJob(context.Context, string, string) error
	FailJob(context.Context, string, string, string, time.Time, bool) error
	RecoverTriageJobs(context.Context) (int, error)
}

// AtomicIntakeStore persists the accepted incident, its intake audit event,
// and the first workflow job as one durable unit before a webhook is acknowledged.
type AtomicIntakeStore interface {
	CreateIncidentIntake(context.Context, domain.Incident, domain.AuditEvent, domain.WorkflowJob) error
}

// ConditionalStateStore prevents stale workers from overwriting a newer
// incident state observed after their work began.
type ConditionalStateStore interface {
	CompareAndSwapIncidentState(context.Context, string, domain.IncidentState, domain.IncidentState) (bool, error)
}

type ExecutionRecoveryStore interface {
	ListExecutionRecordsByStatus(context.Context, string) ([]domain.ExecutionRecord, error)
}

type AtomicApprovalStore interface {
	DecideApproval(context.Context, string, domain.ApprovalRecord, domain.CandidateActionStatus, domain.CandidateActionStatus, domain.IncidentState, domain.IncidentState) (bool, error)
}
