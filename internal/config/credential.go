package config

import (
	"os"
	"path/filepath"
	"strings"
)

func DefaultCredentialKeyPath() string {
	configDir, err := os.UserConfigDir()
	if err != nil || strings.TrimSpace(configDir) == "" {
		return filepath.Join(".triovexa", "credential.key")
	}
	return filepath.Join(configDir, "Triovexa", "credential.key")
}
