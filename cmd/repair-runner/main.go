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

	"github.com/Cyaside/Triovexa/internal/ai"
	"github.com/Cyaside/Triovexa/internal/coderepair/agent"
	repairrunner "github.com/Cyaside/Triovexa/internal/coderepair/runner"
	"github.com/Cyaside/Triovexa/internal/coderepair/sandbox"
	appconfig "github.com/Cyaside/Triovexa/internal/config"
	"github.com/Cyaside/Triovexa/internal/secretstore"
	"github.com/Cyaside/Triovexa/internal/security"
	"github.com/Cyaside/Triovexa/internal/storage"
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
	image := strings.TrimSpace(os.Getenv("REPAIR_SANDBOX_IMAGE"))
	if image == "" {
		return errors.New("REPAIR_SANDBOX_IMAGE must identify a prebuilt isolated test image")
	}
	store, err := storage.NewPostgresStore(cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := loadReasoningConnection(context.Background(), store, &cfg); err != nil {
		return err
	}
	provider, baseURL, apiKey, model := cfg.EffectiveLLM()
	client, err := ai.NewOpenAICompatibleClient(ai.ProviderConfig{
		Name: provider, BaseURL: baseURL, APIKey: apiKey, Model: model,
		JSONMode: cfg.LLMJSONMode, Timeout: cfg.LLMTimeout,
		AllowHTTP:  strings.EqualFold(cfg.Environment, "local") || strings.EqualFold(cfg.Environment, "local-demo"),
		AllowHosts: cfg.LLMAllowHosts, RequireAllowlist: cfg.InternalMode(),
	})
	if err != nil {
		return err
	}
	if !client.Configured() {
		return errors.New("code repair requires a configured OpenAI-compatible provider")
	}
	loop, err := agent.NewLoop(client, sandbox.DockerTester{Image: image})
	if err != nil {
		return err
	}
	checkoutParent, err := os.MkdirTemp("", "triovexa-repair-checkouts-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(checkoutParent)
	handler, err := agent.NewHandler(store, loop, agent.DefaultCheckoutFactory(checkoutParent))
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
	logger.Info("repair investigation runner started", "provider", provider, "model", model)
	return runner.Run(ctx)
}

type settingReader interface {
	GetSetting(context.Context, string) (string, error)
}

func loadReasoningConnection(ctx context.Context, store settingReader, cfg *appconfig.Config) error {
	raw, err := store.GetSetting(ctx, appconfig.ReasoningConnectionBundleSettingKey)
	if errors.Is(err, storage.ErrNotFound) {
		profileRaw, profileErr := store.GetSetting(ctx, appconfig.ReasoningConnectionSettingKey)
		if errors.Is(profileErr, storage.ErrNotFound) {
			return nil // Environment configuration remains the source in this case.
		}
		if profileErr != nil {
			return profileErr
		}
		profile, decodeErr := appconfig.DecodeReasoningConnectionProfile(profileRaw)
		if decodeErr != nil {
			return decodeErr
		}
		appconfig.ApplyReasoningConnectionProfile(cfg, profile)
		return nil
	}
	if err != nil {
		return err
	}
	bundle, err := appconfig.DecodeReasoningConnectionBundle(raw)
	if err != nil {
		return err
	}
	if cfg.CredentialEncryptionKey == "" {
		if _, err := os.Stat(cfg.CredentialKeyPath); err != nil {
			return errors.New("stored reasoning connection requires the existing credential key file")
		}
	}
	cipher, err := secretstore.NewCipher(cfg.CredentialKeyPath, cfg.CredentialEncryptionKey)
	if err != nil {
		return err
	}
	apiKey, err := cipher.Decrypt(bundle.EncryptedAPIKey)
	if err != nil {
		return errors.New("stored reasoning credential could not be decrypted")
	}
	appconfig.ApplyReasoningConnectionProfile(cfg, bundle.Profile)
	cfg.LLMAPIKey = apiKey
	return nil
}
