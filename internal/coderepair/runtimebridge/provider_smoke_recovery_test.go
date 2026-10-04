package runtimebridge

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

type smokeDatabaseIdentity struct{ cluster, database string }

func validateSmokeDatabaseIdentity(shared, isolated smokeDatabaseIdentity, expected string) error {
	if expected == "" || !strings.Contains(expected, "test") || isolated.database != expected || shared.database == "" ||
		shared.cluster == "" || isolated.cluster == "" || shared == isolated {
		return errors.New("provider validation requires an explicitly selected separate test database")
	}
	return nil
}

// Compare physical PostgreSQL identities, not host spelling or connection URLs.
// An alias or a different credential cannot make the shared database isolated.
func validateSmokeDatabaseIsolation(ctx context.Context, shared, isolated *sql.DB, expected string) error {
	var a, b smokeDatabaseIdentity
	read := func(db *sql.DB, identity *smokeDatabaseIdentity) error {
		return db.QueryRowContext(ctx, `SELECT system_identifier::text, current_database() FROM pg_control_system()`).Scan(&identity.cluster, &identity.database)
	}
	if read(shared, &a) != nil || read(isolated, &b) != nil {
		return errors.New("database isolation could not be verified with read-only cluster metadata")
	}
	return validateSmokeDatabaseIdentity(a, b, expected)
}

func openSmokeTestDatabase(dsn, expected string) (*sql.DB, error) {
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Path != "/"+expected || expected == "" ||
		!strings.Contains(expected, "test") || (parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost" && parsed.Hostname() != "::1") {
		return nil, errors.New("an explicitly named loopback test database is required")
	}
	for key := range parsed.Query() {
		if key != "sslmode" {
			return nil, errors.New("test database connection overrides are not permitted")
		}
	}
	return sql.Open("pgx", dsn)
}

func TestProviderSmokeDatabaseIdentityRejectsSharedOrUnconfirmedDatabase(t *testing.T) {
	shared := smokeDatabaseIdentity{"cluster-a", "application_test"}
	for _, scenario := range []struct {
		name, cluster, database, expected string
		valid                             bool
	}{
		{"same physical database", "cluster-a", "application_test", "application_test", false},
		{"different database", "cluster-a", "isolated_test", "isolated_test", true},
		{"different cluster", "cluster-b", "application_test", "application_test", true},
		{"unconfirmed ownership", "cluster-a", "isolated_test", "", false},
		{"unexpected database", "cluster-a", "other_test", "isolated_test", false},
		{"missing physical identity", "", "isolated_test", "isolated_test", false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			err := validateSmokeDatabaseIdentity(shared, smokeDatabaseIdentity{scenario.cluster, scenario.database}, scenario.expected)
			if (err == nil) != scenario.valid {
				t.Fatalf("database isolation: %v", err)
			}
		})
	}
	for _, dsn := range []string{"postgres://localhost/application_test?host=elsewhere", "postgres://remote/isolated_test", "postgres://localhost/isolated_test?dbname=application_test"} {
		if db, err := openSmokeTestDatabase(dsn, "isolated_test"); err == nil {
			_ = db.Close()
			t.Fatal("unsafe test DSN accepted")
		}
	}
}

func TestProviderSmokeRejectsSharedDatabaseIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("dedicated TEST_DATABASE_URL is required")
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal("test database URL is invalid")
	}
	expected := strings.TrimPrefix(parsed.Path, "/")
	db, err := openSmokeTestDatabase(dsn, expected)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := validateSmokeDatabaseIsolation(ctx, db, db, expected); err == nil || !strings.Contains(err.Error(), "explicitly selected separate test database") {
		t.Fatal("same physical database passed isolation")
	}
}

func TestProviderSmokeFixtureRetainsRecoveryStateIntegration(t *testing.T) {
	var retained persistedFixture
	t.Run("retained lifecycle", func(t *testing.T) {
		retained = persistedNativeFixtureRetained(t, true, nil)
		if _, err := retained.admin.Exec(`INSERT INTO ` + retained.appSchema + `.application_settings(key,value) VALUES('smoke-retention-proof','persisted')`); err != nil {
			t.Fatal(err)
		}
	})
	if retained.appSchema == "" {
		t.Skip("isolated native dependencies unavailable")
	}
	// The nested test has closed its pools. Open only the same dedicated test
	// database and prove that retained state outlives the fixture's cleanup.
	db, err := sql.Open("pgx", os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	defer db.Exec(`DROP SCHEMA IF EXISTS ` + retained.appSchema + ` CASCADE`)
	defer db.Exec(`DROP ROLE IF EXISTS ` + retained.role)
	defer db.Exec(`DROP SCHEMA IF EXISTS ` + retained.checkpoint + ` CASCADE`)
	var value string
	if db.QueryRow(`SELECT value FROM `+retained.appSchema+`.application_settings WHERE key='smoke-retention-proof'`).Scan(&value) != nil || value != "persisted" {
		t.Fatal("live validation fixture removed its retained application state")
	}
	var checkpoint, role bool
	if db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname=$1),EXISTS(SELECT 1 FROM pg_roles WHERE rolname=$2)`, retained.checkpoint, retained.role).Scan(&checkpoint, &role) != nil || !checkpoint || !role {
		t.Fatal("live validation fixture removed checkpoint recovery state")
	}
}
