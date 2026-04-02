package config

import "testing"

func TestDatabaseTarget(t *testing.T) {
	t.Run("returns memory label for in-memory mode", func(t *testing.T) {
		cfg := Config{DatabaseURL: "memory"}

		if got := cfg.DatabaseTarget(); got != "memory" {
			t.Fatalf("DatabaseTarget() = %q, want %q", got, "memory")
		}
	})

	t.Run("returns host and database for postgres url", func(t *testing.T) {
		cfg := Config{DatabaseURL: "postgres://postgres:postgres@localhost:5432/triovexa?sslmode=disable"}

		if got := cfg.DatabaseTarget(); got != "localhost:5432/triovexa" {
			t.Fatalf("DatabaseTarget() = %q, want %q", got, "localhost:5432/triovexa")
		}
	})
}
