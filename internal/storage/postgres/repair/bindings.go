package repair

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/Cyaside/Triovexa/internal/coderepair"
)

func (s *Store) CreateRepositoryBinding(ctx context.Context, binding coderepair.RepositoryBinding) error {
	if err := binding.Validate(); err != nil {
		return err
	}
	paths, err := json.Marshal(binding.AllowedPaths)
	if err != nil {
		return err
	}
	recipes, err := json.Marshal(binding.TestRecipes)
	if err != nil {
		return err
	}
	var profile any
	var automation any
	if binding.Automation != nil {
		encoded, err := json.Marshal(binding.Automation)
		if err != nil {
			return err
		}
		automation = string(encoded)
	}
	if binding.ValidationProfile != nil {
		encoded, encodeErr := json.Marshal(binding.ValidationProfile)
		if encodeErr != nil {
			return encodeErr
		}
		profile = string(encoded)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO repository_bindings
		(id, service_name, environment, repository_url, base_ref, allowed_paths_json, test_recipes_json, policy_version, enabled, created_at, updated_at, validation_profile_json, credential_ref, automation_json)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12::jsonb,$13,$14::jsonb)`,
		binding.ID, binding.ServiceName, binding.Environment, binding.RepositoryURL, binding.BaseRef,
		string(paths), string(recipes), binding.PolicyVersion, binding.Enabled, binding.CreatedAt.UTC(), binding.UpdatedAt.UTC(), profile, binding.CredentialRef, automation)
	return err
}

func scanRepositoryBinding(row repairScanner) (coderepair.RepositoryBinding, error) {
	var binding coderepair.RepositoryBinding
	var paths, recipes string
	var profile sql.NullString
	var automation sql.NullString
	err := row.Scan(&binding.ID, &binding.ServiceName, &binding.Environment, &binding.RepositoryURL,
		&binding.BaseRef, &paths, &recipes, &binding.PolicyVersion, &binding.Enabled, &binding.CreatedAt, &binding.UpdatedAt, &profile, &binding.CredentialRef, &automation)
	if errors.Is(err, sql.ErrNoRows) {
		return coderepair.RepositoryBinding{}, ErrNotFound
	}
	if err != nil {
		return coderepair.RepositoryBinding{}, err
	}
	if err := json.Unmarshal([]byte(paths), &binding.AllowedPaths); err != nil {
		return coderepair.RepositoryBinding{}, err
	}
	if err := json.Unmarshal([]byte(recipes), &binding.TestRecipes); err != nil {
		return coderepair.RepositoryBinding{}, err
	}
	if profile.Valid {
		if err := json.Unmarshal([]byte(profile.String), &binding.ValidationProfile); err != nil {
			return coderepair.RepositoryBinding{}, err
		}
	}
	if automation.Valid {
		if err := json.Unmarshal([]byte(automation.String), &binding.Automation); err != nil {
			return coderepair.RepositoryBinding{}, err
		}
	}
	return binding, nil
}

const repairBindingColumns = `id, service_name, environment, repository_url, base_ref,
	allowed_paths_json::text, test_recipes_json::text, policy_version, enabled, created_at, updated_at, validation_profile_json::text, credential_ref, automation_json::text`

func (s *Store) GetActiveRepositoryBinding(ctx context.Context, service, environment string) (coderepair.RepositoryBinding, error) {
	binding, err := scanRepositoryBinding(s.db.QueryRowContext(ctx, `SELECT `+repairBindingColumns+`
		FROM repository_bindings WHERE service_name=$1 AND environment=$2 AND enabled`, service, environment))
	if errors.Is(err, ErrNotFound) {
		return binding, errors.Join(ErrNotFound, coderepair.ErrRepositoryBindingNotFound)
	}
	return binding, err
}

func (s *Store) GetRepositoryBinding(ctx context.Context, id string) (coderepair.RepositoryBinding, error) {
	return scanRepositoryBinding(s.db.QueryRowContext(ctx, `SELECT `+repairBindingColumns+`
		FROM repository_bindings WHERE id=$1`, id))
}

func getRepairBindingTx(ctx context.Context, tx *sql.Tx, id string) (coderepair.RepositoryBinding, error) {
	return scanRepositoryBinding(tx.QueryRowContext(ctx, `SELECT `+repairBindingColumns+`
		FROM repository_bindings WHERE id=$1`, id))
}

func (s *Store) DisableRepositoryBinding(ctx context.Context, id string) (bool, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE repository_bindings SET enabled=false,updated_at=now() WHERE id=$1 AND enabled`, id)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (s *Store) ListRepositoryBindings(ctx context.Context) ([]coderepair.RepositoryBinding, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+repairBindingColumns+` FROM repository_bindings ORDER BY service_name,environment,created_at DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	bindings := []coderepair.RepositoryBinding{}
	for rows.Next() {
		binding, err := scanRepositoryBinding(rows)
		if err != nil {
			return nil, err
		}
		bindings = append(bindings, binding)
	}
	return bindings, rows.Err()
}
