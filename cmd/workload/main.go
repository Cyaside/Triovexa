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
	"time"

	"github.com/Cyaside/Triovexa/internal/workload"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	client := workload.RedisClient(env("REDIS_ADDRESS", "127.0.0.1:6379"))
	defer client.Close()
	mode := strings.ToLower(env("WORKLOAD_MODE", "supervisor"))
	if len(os.Args) > 1 {
		mode = strings.ToLower(os.Args[1])
	}
	var err error
	switch mode {
	case "producer":
		err = workload.RunProducer(ctx, client, workload.DurationEnv("PRODUCER_INTERVAL", time.Second), logger)
	case "worker":
		err = workload.RunWorker(ctx, client, logger)
	case "supervisor":
		supervisor := workload.NewSupervisor(client, env("CONTROL_API_TOKEN", "local-control-token"), logger)
		if err = supervisor.StartWorker(ctx); err != nil {
			break
		}
		server := &http.Server{Addr: ":" + workload.PortEnv("CONTROL_PORT", "8091"), Handler: supervisor.Handler(ctx), ReadHeaderTimeout: 5 * time.Second}
		go func() {
			<-ctx.Done()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdownCtx)
		}()
		err = server.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
	default:
		logger.Error("unknown workload mode", "mode", mode)
		os.Exit(2)
	}
	if err != nil {
		logger.Error("workload stopped", "error", err)
		os.Exit(1)
	}
}
func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
