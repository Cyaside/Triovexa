//go:build provider_smoke

package runtimebridge

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"regexp"
	"testing"

	"github.com/Cyaside/Triovexa/internal/ai/admission"
	"github.com/Cyaside/Triovexa/internal/storage/postgres/aibudget"
)

// A diagnosed runtime fix must reuse the retained provider-managed accounting,
// even when its domain/checkpoint fixtures get a fresh isolated schema.
func retainedProviderSmokeLedger(t *testing.T, isolated *sql.DB, schema, campaignID string) *admission.Service {
	t.Helper()
	if !regexp.MustCompile(`^native_app_[a-f0-9]{32}$`).MatchString(schema) {
		t.Fatal("retained accounting schema is invalid")
	}
	var exists bool
	if isolated.QueryRow(`SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=$1 AND table_name='ai_budget_campaigns' AND column_name='provider_managed')`, schema).Scan(&exists) != nil || !exists {
		t.Fatal("retained provider-managed ledger is unavailable")
	}
	dsn, err := url.Parse(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal("isolated accounting database is invalid")
	}
	query := dsn.Query()
	query.Set("search_path", schema)
	dsn.RawQuery = query.Encode()
	db, err := sql.Open("pgx", dsn.String())
	if err != nil {
		t.Fatal("retained accounting pool is unavailable")
	}
	t.Cleanup(func() { _ = db.Close() })
	ledger, err := admission.NewService(aibudget.New(db))
	if err != nil {
		t.Fatal(err)
	}
	if retained, err := ledger.GetCampaign(context.Background(), campaignID); err != nil || !retained.ProviderManaged {
		t.Fatal("the selected accounting schema must contain the existing provider-managed campaign")
	}
	return ledger
}
