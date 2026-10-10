package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/coderepair/agent"
	"github.com/Cyaside/Triovexa/internal/coderepair/publisher"
	"github.com/Cyaside/Triovexa/internal/config"
	"github.com/Cyaside/Triovexa/internal/security"
	"github.com/Cyaside/Triovexa/internal/storage/postgres"
)

func main() {
	logger := slog.New(security.NewRedactingHandler(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(logger); err != nil {
		logger.Error("repair publisher stopped", "error", security.Redact(err.Error()))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg := config.Load()
	if cfg.DatabaseURL == "" || strings.EqualFold(cfg.DatabaseURL, "memory") {
		return errors.New("repair publisher requires PostgreSQL")
	}
	github, err := publisher.NewGitHub(os.Getenv("REPAIR_GITHUB_TOKEN"))
	if err != nil {
		return err
	}
	store, err := postgres.NewPostgresStore(cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer store.Close()
	parent, err := os.MkdirTemp("", "triovexa-publisher-checkouts-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(parent)
	checkout := publisher.Checkout(agent.DefaultCheckoutFactory(parent))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger.Info("repair publisher started", "checkout_parent", filepath.Base(parent))
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		claimCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		p, err := store.ClaimRepairPublication(claimCtx, "repair-publisher", time.Now().UTC(), 5*time.Minute)
		cancel()
		if errors.Is(err, coderepair.ErrNoJobAvailable) {
			continue
		}
		if err != nil {
			logger.Error("publication claim failed", "error", security.Redact(err.Error()))
			continue
		}
		workCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		input, err := store.GetRepairPublicationInput(workCtx, p)
		var pr publisher.PullRequest
		var head string
		if err == nil {
			service, serviceErr := publisher.NewService(github, checkout, func(ctx context.Context) error {
				if cfg.KillSwitchEnabled {
					return errors.New("publication blocked by kill switch")
				}
				return store.CheckRepairPublicationSafety(ctx, p.CaseID, time.Now().UTC())
			})
			if serviceErr != nil {
				err = serviceErr
			} else {
				pr, head, err = service.Publish(workCtx, p, publisher.Input{
					Case: input.Case, Attempt: input.Attempt, Binding: input.Binding,
					Approval: input.Approval, Patch: input.Patch, ReportJSON: input.ReportJSON,
				}, time.Now().UTC())
			}
		}
		cancel()
		resultCtx, resultCancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err != nil {
			logger.Error("publication attempt failed", "case_id", p.CaseID, "error", security.Redact(err.Error()))
			_, _ = store.FailRepairPublication(resultCtx, p, err.Error(), time.Now().UTC())
		} else if committed, commitErr := store.CompleteRepairPublication(resultCtx, p, head, pr.Number, pr.URL, time.Now().UTC()); commitErr != nil || !committed {
			logger.Error("publication result not committed", "case_id", p.CaseID, "error", commitErr)
		} else {
			logger.Info("repair pull request recorded", "case_id", p.CaseID, "pr_number", pr.Number)
		}
		resultCancel()
	}
}
