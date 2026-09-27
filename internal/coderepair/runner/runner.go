package runner

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/security"
)

type Handler func(context.Context, coderepair.Job) error

// PermanentError ends the current attempt without retrying the same job.
// Provider and sandbox adapters should use it only for a classified failure.
type PermanentError struct{ Err error }

func (e PermanentError) Error() string { return e.Err.Error() }
func (e PermanentError) Unwrap() error { return e.Err }

type Config struct {
	Workers      int
	Lease        time.Duration
	MaxRun       time.Duration
	PollInterval time.Duration
}

type Runner struct {
	store   coderepair.JobStore
	handler Handler
	logger  *slog.Logger
	config  Config
}

func New(store coderepair.JobStore, handler Handler, logger *slog.Logger, config Config) (*Runner, error) {
	if store == nil || handler == nil {
		return nil, errors.New("repair runner requires a store and handler")
	}
	if config.Workers <= 0 {
		config.Workers = 1
	}
	if config.Lease <= 0 {
		config.Lease = 45 * time.Second
	}
	if config.Lease < 3*time.Millisecond {
		return nil, errors.New("repair runner lease is too short to renew safely")
	}
	if config.MaxRun <= 0 {
		config.MaxRun = 20 * time.Minute
	}
	if config.PollInterval <= 0 {
		config.PollInterval = time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{store: store, handler: handler, logger: logger, config: config}, nil
}

// Run blocks until shutdown. Recovery only dead-letters expired leases that
// exhausted their attempt budget; remaining leases become claimable on expiry.
func (r *Runner) Run(ctx context.Context) error {
	if _, err := r.store.RecoverRepairJobs(ctx, time.Now().UTC()); err != nil {
		return err
	}
	var workers sync.WaitGroup
	for i := 0; i < r.config.Workers; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			r.runWorker(ctx, "repair-"+uuid.NewString())
		}()
	}
	workers.Wait()
	return nil
}

func (r *Runner) runWorker(ctx context.Context, workerID string) {
	ticker := time.NewTicker(r.config.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			job, err := r.store.ClaimRepairJob(ctx, workerID, time.Now().UTC(), r.config.Lease)
			if errors.Is(err, coderepair.ErrNoJobAvailable) {
				continue
			}
			if err != nil {
				r.logger.Error("claim repair job", "error", security.Redact(err.Error()))
				continue
			}
			r.processJob(ctx, job)
		}
	}
}

func (r *Runner) processJob(ctx context.Context, job coderepair.Job) {
	jobCtx, cancel := context.WithTimeout(ctx, r.config.MaxRun)
	leaseLost := make(chan struct{}, 1)
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(r.config.Lease / 3)
		defer ticker.Stop()
		for {
			select {
			case <-jobCtx.Done():
				return
			case <-ticker.C:
				ok, err := r.store.RenewRepairJobLease(jobCtx, job.ID, job.LeaseToken, time.Now().UTC(), r.config.Lease)
				if err != nil || !ok {
					if err != nil {
						r.logger.Error("renew repair lease", "job_id", job.ID, "error", security.Redact(err.Error()))
					}
					leaseLost <- struct{}{}
					cancel()
					return
				}
			}
		}
	}()
	workErr := r.handler(jobCtx, job)
	runErr := jobCtx.Err()
	cancel()
	<-heartbeatDone
	select {
	case <-leaseLost:
		return
	default:
	}
	if ctx.Err() != nil {
		return // shutdown: the lease will expire and be reconciled on restart
	}
	if workErr == nil && runErr != nil {
		workErr = runErr
	}
	now := time.Now().UTC()
	if workErr == nil {
		if ok, err := r.store.CompleteRepairJob(ctx, job.ID, job.LeaseToken, now); err != nil || !ok {
			r.logger.Error("complete repair job", "job_id", job.ID, "error", err, "lease_valid", ok)
		}
		return
	}
	var permanent PermanentError
	terminal := job.Attempts >= job.MaxAttempts || errors.As(workErr, &permanent)
	retryAt := now.Add(time.Duration(job.Attempts*job.Attempts) * time.Second)
	if ok, err := r.store.FailRepairJob(ctx, job.ID, job.LeaseToken, workErr.Error(), now, retryAt, terminal); err != nil || !ok {
		r.logger.Error("fail repair job", "job_id", job.ID, "error", err, "lease_valid", ok)
	}
}
