package repair

import "database/sql"

// Store owns code-repair persistence and uses the PostgreSQL connection owned
// by the outer store, including its migrations and pool lifecycle.
type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store { return &Store{db: db} }
