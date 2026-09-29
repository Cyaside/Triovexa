package connections

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Cyaside/Triovexa/internal/config"
	"github.com/Cyaside/Triovexa/internal/secretstore"
	"github.com/Cyaside/Triovexa/internal/storage"
)

type settingReader interface {
	GetSetting(context.Context, string) (string, error)
}

// LoadReasoning applies a saved connection. A corrupt saved connection must
// prevent startup rather than silently selecting a different provider or key.
func LoadReasoning(ctx context.Context, settings settingReader, cfg *config.Config) error {
	raw, err := settings.GetSetting(ctx, config.ReasoningConnectionBundleSettingKey)
	if errors.Is(err, storage.ErrNotFound) {
		return loadLegacyReasoning(ctx, settings, cfg)
	}
	if err != nil {
		return fmt.Errorf("load reasoning connection: %w", err)
	}
	bundle, err := config.DecodeReasoningConnectionBundle(raw)
	if err != nil {
		return fmt.Errorf("decode stored reasoning connection: %w", err)
	}
	if strings.TrimSpace(cfg.CredentialEncryptionKey) == "" {
		if _, err := os.Stat(cfg.CredentialKeyPath); err != nil {
			return errors.New("stored reasoning connection requires the existing credential key file")
		}
	}
	cipher, err := secretstore.NewCipher(cfg.CredentialKeyPath, cfg.CredentialEncryptionKey)
	if err != nil {
		return fmt.Errorf("load reasoning credential key: %w", err)
	}
	apiKey, err := cipher.Decrypt(bundle.EncryptedAPIKey)
	if err != nil {
		return errors.New("stored reasoning credential could not be decrypted")
	}
	config.ApplyReasoningConnectionProfile(cfg, bundle.Profile)
	cfg.LLMAPIKey = apiKey
	return nil
}

func loadLegacyReasoning(ctx context.Context, settings settingReader, cfg *config.Config) error {
	raw, err := settings.GetSetting(ctx, config.ReasoningConnectionSettingKey)
	if errors.Is(err, storage.ErrNotFound) {
		return nil // No stored connection; environment configuration is authoritative.
	}
	if err != nil {
		return fmt.Errorf("load legacy reasoning connection: %w", err)
	}
	profile, err := config.DecodeReasoningConnectionProfile(raw)
	if err != nil {
		return fmt.Errorf("decode legacy reasoning connection: %w", err)
	}
	config.ApplyReasoningConnectionProfile(cfg, profile)
	return nil
}

func LoadGrafana(ctx context.Context, settings settingReader, cfg *config.Config) error {
	raw, err := settings.GetSetting(ctx, config.GrafanaConnectionSettingKey)
	if errors.Is(err, storage.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load Grafana connection: %w", err)
	}
	profile, err := config.DecodeGrafanaConnectionProfile(raw)
	if err != nil {
		return fmt.Errorf("decode stored Grafana connection: %w", err)
	}
	config.ApplyGrafanaConnectionProfile(cfg, profile)
	return nil
}
