package workflow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/storage"
)

type Handler func(context.Context, domain.WorkflowJob) error

type Runner struct {
	store        storage.DurableJobStore
	handler      Handler
	logger       *slog.Logger
	workers      int
	lease        time.Duration
	pollInterval time.Duration
}

func NewRunner(store storage.DurableJobStore, handler Handler, logger *slog.Logger, workers int) *Runner {
	return NewRunnerWithLease(store, handler, logger, workers, 45*time.Second)
}

func NewRunnerWithLease(store storage.DurableJobStore, handler Handler, logger *slog.Logger, workers int, lease time.Duration) *Runner {
	if workers <= 0 {
		workers = 2
	}
	if logger == nil {
		logger = slog.Default()
	}
	if lease <= 0 {
		lease = 45 * time.Second
	}
	return &Runner{store: store, handler: handler, logger: logger, workers: workers, lease: lease, pollInterval: 500 * time.Millisecond}
}

func (r *Runner) Start(ctx context.Context) {
	for index := 0; index < r.workers; index++ {
		go r.runWorker(ctx, fmt.Sprintf("triovexa-%d", index+1))
	}
}

func (r *Runner) runWorker(ctx context.Context, workerID string) {
	ticker := time.NewTicker(r.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			job, err := r.store.ClaimJob(ctx, workerID, time.Now().UTC().Add(r.lease))
			if errors.Is(err, storage.ErrNoJobAvailable) {
				continue
			}
			if err != nil {
				r.logger.Error("claim workflow job", slog.String("error", err.Error()))
				continue
			}
			jobCtx, cancel := context.WithTimeout(ctx, r.lease)
			handleErr := r.handler(jobCtx, job)
			cancel()
			if handleErr == nil {
				if err := r.store.CompleteJob(ctx, job.ID, workerID); err != nil {
					r.logger.Error("complete workflow job", slog.String("job_id", job.ID), slog.String("error", err.Error()))
				}
				continue
			}
			terminal := job.Attempts >= job.MaxAttempts
			retryAt := time.Now().UTC().Add(time.Duration(job.Attempts*job.Attempts) * time.Second)
			if err := r.store.FailJob(ctx, job.ID, workerID, handleErr.Error(), retryAt, terminal); err != nil {
				r.logger.Error("fail workflow job", slog.String("job_id", job.ID), slog.String("error", err.Error()))
			}
		}
	}
}
