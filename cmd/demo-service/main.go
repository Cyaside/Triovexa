package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Cyaside/Triovexa/internal/demo"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	server := demo.NewServer(demo.Config{
		Address: ":" + getEnv("DEMO_SERVICE_PORT", "8090"),
	}, logger)

	logger.Info("starting demo incident source", slog.String("addr", server.Addr))

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
		logger.Error("demo service crashed", slog.String("error", err.Error()))
		os.Exit(1)
	case <-ctx.Done():
		logger.Info("demo service shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("demo service shutdown failed", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}

	return fallback
}
