package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	appconfig "github.com/Cyaside/Triovexa/internal/config"
	apphttp "github.com/Cyaside/Triovexa/internal/http"
	"github.com/Cyaside/Triovexa/internal/incident"
	"github.com/Cyaside/Triovexa/internal/observability"
	"github.com/Cyaside/Triovexa/internal/retrieval"
	"github.com/Cyaside/Triovexa/internal/storage"
	"github.com/Cyaside/Triovexa/internal/triage"
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
	generator := triage.NewHeuristicGenerator()
	incidentService := incident.NewService(repository, collector, retriever, generator)
	server := apphttp.NewServer(cfg, logger, repository, incidentService)

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
