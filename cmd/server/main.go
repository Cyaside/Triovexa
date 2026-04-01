package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/Cyaside/Triovexa/internal/approval"
	appconfig "github.com/Cyaside/Triovexa/internal/config"
	"github.com/Cyaside/Triovexa/internal/execution"
	apphttp "github.com/Cyaside/Triovexa/internal/http"
	"github.com/Cyaside/Triovexa/internal/incident"
	"github.com/Cyaside/Triovexa/internal/observability"
	"github.com/Cyaside/Triovexa/internal/policy"
	"github.com/Cyaside/Triovexa/internal/remediation"
	"github.com/Cyaside/Triovexa/internal/retrieval"
	"github.com/Cyaside/Triovexa/internal/storage"
	"github.com/Cyaside/Triovexa/internal/telemetry"
	"github.com/Cyaside/Triovexa/internal/triage"
	"github.com/Cyaside/Triovexa/internal/verification"
)

func main() {
	cfg := appconfig.Load()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: cfg.LogLevel,
	}))

	repository, err := storage.NewPostgresStore(cfg.DatabaseURL)
	if err != nil {
		logger.Error("failed to initialize storage", slog.String("error", err.Error()))
		os.Exit(1)
	}
	defer func() {
		if err := repository.Close(); err != nil {
			logger.Error("failed to close storage", slog.String("error", err.Error()))
		}
	}()

	collector := observability.NewDemoCollector(cfg.DemoServiceBaseURL)
	retriever := retrieval.NewFileRetriever(cfg.DocsRoot)
	catalog := execution.DefaultCatalog()
	recorder := telemetry.NewRecorder()
	generator := triage.NewHeuristicGenerator()
	actionGenerator := remediation.NewHeuristicGenerator(catalog)
	killSwitch := approval.NewKillSwitch(cfg.KillSwitchEnabled)
	recorder.RecordKillSwitchState(cfg.KillSwitchEnabled)
	policyService := approval.NewService(repository, policy.NewEvaluator(catalog), killSwitch).WithTelemetry(recorder)
	rollbackService := execution.NewRollbackService(repository, catalog, execution.NewDemoAdapter(cfg.DemoServiceBaseURL))
	verificationService := verification.NewService(repository, verification.NewDemoSnapshotFetcher(cfg.DemoServiceBaseURL), catalog, rollbackService).WithTelemetry(recorder)
	executionService := execution.NewService(
		repository,
		catalog,
		execution.NewDemoAdapter(cfg.DemoServiceBaseURL),
		killSwitch,
		verificationService,
		cfg.ActionExecutionTimeout,
		cfg.ActionExecutionRetries,
		cfg.ActionExecutionCooldown,
	).WithTelemetry(recorder)
	incidentService := incident.NewService(repository, collector, retriever, generator, actionGenerator, policyService).WithTelemetry(recorder)
	server := apphttp.NewServerWithTelemetry(cfg, logger, repository, incidentService, policyService, executionService, recorder)

	logger.Info("starting server",
		slog.String("addr", server.Addr),
		slog.String("environment", cfg.Environment),
		slog.Bool("kill_switch_enabled", cfg.KillSwitchEnabled),
		slog.String("database_target", cfg.DatabaseTarget()),
	)

	errCh := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-errCh:
		logger.Error("server crashed", slog.String("error", err.Error()))
		os.Exit(1)
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", slog.String("error", err.Error()))
		os.Exit(1)
	}

	logger.Info("server stopped cleanly")
}
