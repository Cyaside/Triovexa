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
)

func main() {
	cfg := appconfig.Load()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: cfg.LogLevel,
	}))

	server := apphttp.NewServer(cfg, logger)

	logger.Info("starting server",
		slog.String("addr", server.Addr),
		slog.String("environment", cfg.Environment),
		slog.Bool("kill_switch_enabled", cfg.KillSwitchEnabled),
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
