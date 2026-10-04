package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Cyaside/Triovexa/internal/ai/admission"
	"github.com/Cyaside/Triovexa/internal/ai/modelgateway"
	"github.com/Cyaside/Triovexa/internal/coderepair/agent"
	repairrunner "github.com/Cyaside/Triovexa/internal/coderepair/runner"
	"github.com/Cyaside/Triovexa/internal/coderepair/runtimebridge"
	"github.com/Cyaside/Triovexa/internal/coderepair/sandbox"
	appconfig "github.com/Cyaside/Triovexa/internal/config"
	"github.com/Cyaside/Triovexa/internal/secretstore"
	"github.com/Cyaside/Triovexa/internal/security"
	"github.com/Cyaside/Triovexa/internal/storage/postgres"
)

func main() {
	logger := slog.New(security.NewRedactingHandler(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(logger); err != nil {
		logger.Error("repair runner stopped", "error", security.Redact(err.Error()))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg := appconfig.Load()
	if strings.EqualFold(strings.TrimSpace(cfg.DatabaseURL), "memory") || cfg.DatabaseURL == "" {
		return errors.New("code repair runner requires PostgreSQL")
	}
	budgetConfig, err := modelgateway.LoadBudgetConfig(strings.TrimSpace(os.Getenv("AI_BUDGET_CONFIG_PATH")))
	if err != nil {
		return err
	}
	image := strings.TrimSpace(os.Getenv("REPAIR_SANDBOX_IMAGE"))
	if image == "" {
		return errors.New("REPAIR_SANDBOX_IMAGE must identify a prebuilt isolated test image")
	}
	entry := strings.TrimSpace(os.Getenv("REPAIR_AGENT_ENTRY"))
	if !filepath.IsAbs(entry) {
		return errors.New("REPAIR_AGENT_ENTRY must be an absolute path to the built native agent")
	}
	if info, err := os.Stat(entry); err != nil || info.IsDir() {
		return errors.New("native agent entry is unavailable; build agent-runtime first")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		return errors.New("native repair runtime requires Node.js 24")
	}
	checkpointDSN := strings.TrimSpace(os.Getenv("REPAIR_CHECKPOINT_DATABASE_URL"))
	checkpointSchema := strings.TrimSpace(os.Getenv("REPAIR_CHECKPOINT_SCHEMA"))
	if checkpointDSN == "" || checkpointSchema == "" {
		return errors.New("repair runtime requires initialized checkpoint storage with a dedicated restricted role")
	}
	store, err := postgres.NewPostgresStore(cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer store.Close()
	ledger, err := admission.NewService(store.ModelBudgetStore())
	if err != nil {
		return err
	}
	credentialCipher, err := secretstore.NewCipher(cfg.CredentialKeyPath, cfg.CredentialEncryptionKey)
	if err != nil {
		return err
	}
	if err := ledger.CreateCampaign(context.Background(), budgetConfig.Campaign); err != nil {
		return err
	}
	engine := &runtimebridge.NativeRunner{Process: runtimebridge.Process{Executable: node, Entry: entry, Version: runtimebridge.EngineVersion},
		Tests: sandbox.DockerTester{Image: image}, Store: store, Ledger: ledger, Cipher: credentialCipher, CheckpointDSN: checkpointDSN, CheckpointSchema: checkpointSchema}
	checkoutParent, err := os.MkdirTemp("", "triovexa-repair-checkouts-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(checkoutParent)
	handler, err := agent.NewHandler(store, engine, agent.DefaultCheckoutFactory(checkoutParent))
	if err != nil {
		return err
	}
	runner, err := repairrunner.New(store, handler.Handle, logger, repairrunner.Config{
		Workers: 1, Lease: 45 * time.Second, MaxRun: 20 * time.Minute, PollInterval: time.Second,
	})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger.Info("repair investigation runner started", "engine", "deepagents", "engine_version", runtimebridge.EngineVersion)
	return runner.Run(ctx)
}
