package postgres

import (
	"github.com/Cyaside/Triovexa/internal/ai/admission"
	"github.com/Cyaside/Triovexa/internal/storage/postgres/aibudget"
)

// ModelBudgetStore shares the application-owned pool and numbered migrations.
// It exposes no public HTTP endpoint or provider credentials.
func (s *PostgresStore) ModelBudgetStore() admission.Store { return aibudget.New(s.db) }
