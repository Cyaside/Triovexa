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
