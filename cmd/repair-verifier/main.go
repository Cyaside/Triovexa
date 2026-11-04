package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	repairverify "github.com/Cyaside/Triovexa/internal/coderepair/verification"
	"github.com/Cyaside/Triovexa/internal/config"
	"github.com/Cyaside/Triovexa/internal/security"
	"github.com/Cyaside/Triovexa/internal/storage/postgres"
)

func main() {
	logger := slog.New(security.NewRedactingHandler(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(logger); err != nil {
		logger.Error("repair verifier stopped", "error", security.Redact(err.Error()))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg := config.Load()
	if cfg.DatabaseURL == "" || strings.EqualFold(cfg.DatabaseURL, "memory") {
		return errors.New("repair verifier requires PostgreSQL")
	}
	store, err := postgres.NewPostgresStore(cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer store.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		claimCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		d, err := store.ClaimRepairVerification(claimCtx, time.Now().UTC(), 3*time.Minute)
		cancel()
		if errors.Is(err, coderepair.ErrNoJobAvailable) {
			continue
		}
		if err != nil {
			logger.Error("verification claim failed", "error", security.Redact(err.Error()))
			continue
		}
		workCtx, workCancel := context.WithTimeout(ctx, 2*time.Minute)
		client, clientErr := repairverify.NewClient(repairverify.ClientConfig{WorkloadURL: cfg.WorkloadControlBaseURL, WorkloadToken: cfg.WorkloadControlToken, PrometheusURL: cfg.PrometheusBaseURL}, d.Contract)
		recovered, reason := false, "Configured telemetry profile is unavailable"
		if clientErr == nil {
			recovered, reason = observe(workCtx, store, client, d)
		}
		workCancel()
		if ctx.Err() != nil {
			continue
		} // Expired lease is reclaimed after restart.
		resultCtx, resultCancel := context.WithTimeout(context.Background(), 10*time.Second)
		ok, err := store.FinishRepairVerification(resultCtx, d, recovered, reason, time.Now().UTC())
		resultCancel()
		if err != nil || !ok {
			logger.Error("verification result not committed", "case_id", d.CaseID, "error", err)
		}
	}
}

type sampleWriter interface {
	RecordRepairVerificationSample(context.Context, postgres.RepairDeployment, repairverify.Sample, bool, time.Time) error
}

type sampleFetcher interface {
	Snapshot(context.Context) (repairverify.Sample, error)
}

func observe(ctx context.Context, writer sampleWriter, fetcher sampleFetcher, d postgres.RepairDeployment) (bool, string) {
	consecutive := 0
	for {
		now := time.Now().UTC()
		sample, err := fetcher.Snapshot(ctx)
		if err != nil {
			return false, "telemetry missing or stale"
		}
		passed := repairverify.CheckFor(d.Contract, d.Baseline, sample, d.RevisionSHA)
		if err := writer.RecordRepairVerificationSample(ctx, d, sample, passed, now); err != nil {
			return false, "verification sample could not be persisted"
		}
		if passed {
			consecutive++
		} else {
			consecutive = 0
		}
		if consecutive >= 3 {
			return true, "three consecutive recovery observations"
		}
		select {
		case <-ctx.Done():
			return false, "recovery was not proven within two minutes"
		case <-time.After(10 * time.Second):
		}
	}
}
