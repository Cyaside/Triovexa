package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/Cyaside/Triovexa/internal/ai"
	"github.com/Cyaside/Triovexa/internal/approval"
	"github.com/Cyaside/Triovexa/internal/auth"
	appconfig "github.com/Cyaside/Triovexa/internal/config"
	"github.com/Cyaside/Triovexa/internal/execution"
	apphttp "github.com/Cyaside/Triovexa/internal/http"
	"github.com/Cyaside/Triovexa/internal/incident"
	"github.com/Cyaside/Triovexa/internal/mode"
	"github.com/Cyaside/Triovexa/internal/observability"
	"github.com/Cyaside/Triovexa/internal/policy"
	"github.com/Cyaside/Triovexa/internal/remediation"
	"github.com/Cyaside/Triovexa/internal/retrieval"
	"github.com/Cyaside/Triovexa/internal/storage"
	"github.com/Cyaside/Triovexa/internal/telemetry"
	"github.com/Cyaside/Triovexa/internal/triage"
	"github.com/Cyaside/Triovexa/internal/verification"
	"github.com/Cyaside/Triovexa/internal/workflow"
)

func main() {
	cfg := appconfig.Load()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: cfg.LogLevel,
	}))

	var repository storage.Repository
	var err error
	if strings.EqualFold(strings.TrimSpace(cfg.DatabaseURL), "memory") {
		if cfg.InternalMode() {
			logger.Error("internal deployment mode requires PostgreSQL")
			os.Exit(1)
		}
		logger.Warn("starting with in-memory repository; data will be lost on shutdown")
		repository = storage.NewMemoryStore()
	} else {
		repository, err = storage.NewPostgresStore(cfg.DatabaseURL)
		if err != nil {
			logger.Error("failed to initialize storage", slog.String("error", err.Error()))
			os.Exit(1)
		}
	}
	defer func() {
		if err := repository.Close(); err != nil {
			logger.Error("failed to close storage", slog.String("error", err.Error()))
		}
	}()

	retriever := retrieval.NewFileRetriever(cfg.DocsRoot)
	catalog := execution.DefaultCatalog()
	recorder := telemetry.NewRecorder()
	runtimeModes := mode.NewManager(cfg.ReasoningMode, cfg.ObservabilityMode)
	llmProvider, llmBaseURL, llmAPIKey, llmModel := cfg.EffectiveLLM()
	llmClient, err := ai.NewOpenAICompatibleClient(ai.ProviderConfig{
		Name: llmProvider, BaseURL: llmBaseURL, APIKey: llmAPIKey, Model: llmModel,
		JSONMode: cfg.LLMJSONMode, Timeout: cfg.LLMTimeout,
		AllowHTTP:  strings.EqualFold(cfg.Environment, "local") || strings.EqualFold(cfg.Environment, "local-demo"),
		AllowHosts: cfg.LLMAllowHosts,
	})
	if err != nil {
		logger.Error("invalid LLM provider configuration", slog.String("error", err.Error()))
		os.Exit(1)
	}
	grafanaClient := observability.NewGrafanaClient(
		cfg.GrafanaBaseURL,
		cfg.GrafanaAPIToken,
		cfg.GrafanaMetricsSourceUID,
		cfg.GrafanaLogsSourceUID,
	)
	grafanaSignals := observability.GrafanaSignalConfig{
		ErrorRateQuery:  cfg.GrafanaErrorRateQuery,
		LatencyQuery:    cfg.GrafanaLatencyQuery,
		QueueQuery:      cfg.GrafanaQueueQuery,
		ReplicaQuery:    cfg.GrafanaReplicaQuery,
		LogsQuery:       cfg.GrafanaLogsQuery,
		DeployLogsQuery: cfg.GrafanaDeployLogsQuery,
		Lookback:        cfg.GrafanaQueryLookback,
	}
	collector := observability.NewSwitchingCollector(
		runtimeModes,
		observability.NewDemoCollector(cfg.DemoServiceBaseURL),
		observability.NewGrafanaCollector(grafanaClient, grafanaSignals),
	)
	generator := triage.NewSwitchingGenerator(
		runtimeModes,
		triage.NewHeuristicGenerator(),
		triage.NewLLMGenerator(llmClient),
	)
	actionGenerator := remediation.NewSwitchingGenerator(
		runtimeModes,
		remediation.NewHeuristicGenerator(catalog),
		remediation.NewLLMGenerator(catalog, llmClient),
	)
	killSwitch := approval.NewKillSwitch(cfg.KillSwitchEnabled)
	recorder.RecordKillSwitchState(cfg.KillSwitchEnabled)
	policyService := approval.NewService(repository, policy.NewEvaluator(catalog), killSwitch).WithTelemetry(recorder)
	rollbackService := execution.NewRollbackService(repository, catalog, execution.NewDemoAdapter(cfg.DemoServiceBaseURL), cfg.ActionExecutionTimeout)
	verificationService := verification.NewService(
		repository,
		observability.NewSwitchingSnapshotFetcher(
			runtimeModes,
			verification.NewDemoSnapshotFetcher(cfg.DemoServiceBaseURL),
			observability.NewGrafanaSnapshotFetcher(grafanaClient, grafanaSignals),
		),
		catalog,
		rollbackService,
	).WithTelemetry(recorder)
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
	authService := auth.NewService(repository, cfg.SessionTTL)
	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()
	if jobStore, ok := repository.(storage.DurableJobStore); ok {
		if recovered, recoverErr := jobStore.RecoverTriageJobs(appCtx); recoverErr != nil {
			logger.Error("recover triage jobs", slog.String("error", recoverErr.Error()))
			os.Exit(1)
		} else if recovered > 0 {
			logger.Info("recovered triage jobs", slog.Int("count", recovered))
		}
		workflow.NewRunner(jobStore, incidentService.ProcessWorkflowJob, logger, 2).Start(appCtx)
	}
	server := apphttp.NewServerWithTelemetry(
		cfg,
		logger,
		repository,
		incidentService,
		policyService,
		executionService,
		recorder,
		&apphttp.RuntimeControls{
			Modes: runtimeModes,
			Providers: apphttp.ProviderStatus{
				MistralConfigured:  llmClient.Configured() && llmProvider == "mistral",
				GrafanaConfigured:  grafanaClient.Configured(),
				MistralModel:       llmModel,
				LLMConfigured:      llmClient.Configured(),
				LLMProvider:        llmProvider,
				LLMModel:           llmModel,
				MetricsSourceUID:   cfg.GrafanaMetricsSourceUID,
				LogsSourceUID:      cfg.GrafanaLogsSourceUID,
				GrafanaDatasources: grafanaClient,
			},
			Auth: authService,
		},
	)

	logger.Info("starting server",
		slog.String("addr", server.Addr),
		slog.String("environment", cfg.Environment),
		slog.Bool("kill_switch_enabled", cfg.KillSwitchEnabled),
		slog.String("database_target", cfg.DatabaseTarget()),
		slog.String("reasoning_mode", string(runtimeModes.Snapshot().Reasoning)),
		slog.String("observability_mode", string(runtimeModes.Snapshot().Observability)),
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
	appCancel()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", slog.String("error", err.Error()))
		os.Exit(1)
	}

	logger.Info("server stopped cleanly")
}
