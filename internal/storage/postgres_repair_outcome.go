package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/security"
)

// RecordRepairInvestigationOutcome writes the decision, immutable content and
// audit event in one transaction, fenced by the claimed job lease. Completion
// of the job is separate and can be reconciled after a process crash.
func (s *PostgresStore) RecordRepairInvestigationOutcome(ctx context.Context, outcome coderepair.InvestigationOutcome) (bool, error) {
	if outcome.JobID == "" || outcome.LeaseToken == "" || outcome.CaseID == "" || outcome.AttemptID == "" ||
		outcome.ExpectedVersion < 1 || outcome.RecordedAt.IsZero() ||
		len(outcome.ReportJSON) == 0 || len(outcome.ReportJSON) > 128*1024 || !json.Valid(outcome.ReportJSON) ||
		security.Redact(string(outcome.ReportJSON)) != string(outcome.ReportJSON) {
		return false, errors.New("invalid or sensitive repair investigation report")
	}
	if outcome.State != coderepair.StatePatchReady && outcome.State != coderepair.StateBlocked && outcome.State != coderepair.StateFailed {
		return false, errors.New("invalid repair investigation outcome state")
	}
	var report struct {
		Status      coderepair.State `json:"status"`
		RecipeID    string           `json:"recipe_id"`
		PatchSHA256 string           `json:"patch_sha256"`
		BeforeExit  int              `json:"before_exit"`
		AfterExit   int              `json:"after_exit"`
		EvidenceIDs []string         `json:"evidence_ids"`
	}
	if err := json.Unmarshal(outcome.ReportJSON, &report); err != nil || report.Status != outcome.State || report.RecipeID == "" {
		return false, errors.New("repair report does not match outcome")
	}
	if outcome.State == coderepair.StatePatchReady {
		if len(outcome.Patch) == 0 || len(outcome.Patch) > 64*1024 || outcome.ErrorCode != "" || outcome.ErrorMessage != "" ||
			report.BeforeExit == 0 || report.AfterExit != 0 || len(report.EvidenceIDs) == 0 {
			return false, errors.New("verified repair outcome lacks red-to-green proof")
		}
		digest := sha256.Sum256(outcome.Patch)
		if outcome.PatchSHA256 != hex.EncodeToString(digest[:]) || report.PatchSHA256 != outcome.PatchSHA256 ||
			security.Redact(string(outcome.Patch)) != string(outcome.Patch) {
			return false, errors.New("repair patch content does not match its digest or contains secrets")
		}
	} else if len(outcome.Patch) != 0 || outcome.PatchSHA256 != "" ||
		strings.TrimSpace(outcome.ErrorCode) == "" || len(outcome.ErrorCode) > 64 ||
		strings.TrimSpace(outcome.ErrorMessage) == "" || len(outcome.ErrorMessage) > 512 ||
		security.Redact(outcome.ErrorMessage) != outcome.ErrorMessage {
		return false, errors.New("failed repair outcome must not contain a fallback patch")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	job, err := scanRepairJob(tx.QueryRowContext(ctx, `SELECT `+repairJobColumns+` FROM repair_jobs WHERE id=$1 FOR UPDATE`, outcome.JobID))
	if err != nil {
		return false, err
	}
	if job.Status != coderepair.JobRunning || job.LeaseToken != outcome.LeaseToken ||
		!job.LeaseUntil.After(outcome.RecordedAt) || !job.LeaseUntil.After(time.Now().UTC()) ||
		job.CaseID != outcome.CaseID || job.AttemptID != outcome.AttemptID {
		return false, nil
	}
	var state coderepair.State
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT state,version FROM repair_cases WHERE id=$1 FOR UPDATE`, outcome.CaseID).
		Scan(&state, &version); err != nil {
		return false, err
	}
	if state != coderepair.StateInvestigating || version != outcome.ExpectedVersion {
		return false, nil
	}
	var attemptStatus string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM repair_attempts WHERE id=$1 AND case_id=$2 FOR UPDATE`,
		outcome.AttemptID, outcome.CaseID).Scan(&attemptStatus); err != nil {
		return false, err
	}
	if attemptStatus != coderepair.JobRunning {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE repair_cases SET state=$1,version=version+1,updated_at=$2 WHERE id=$3`,
		outcome.State, outcome.RecordedAt.UTC(), outcome.CaseID); err != nil {
		return false, err
	}
	if outcome.State != coderepair.StatePatchReady {
		if _, err := tx.ExecContext(ctx, `UPDATE repair_attempts SET error_code=$1,error_message=$2 WHERE id=$3`,
			outcome.ErrorCode, outcome.ErrorMessage, outcome.AttemptID); err != nil {
			return false, err
		}
	}
	if err := insertRepairContentTx(ctx, tx, outcome.AttemptID, "investigation_report", outcome.ReportJSON, outcome.RecordedAt); err != nil {
		return false, err
	}
	if outcome.State == coderepair.StatePatchReady {
		if err := insertRepairContentTx(ctx, tx, outcome.AttemptID, "patch", outcome.Patch, outcome.RecordedAt); err != nil {
			return false, err
		}
	}
	details, _ := json.Marshal(map[string]string{
		"status": string(outcome.State), "code": outcome.ErrorCode, "patch_sha256": outcome.PatchSHA256,
	})
	event := coderepair.Event{ID: uuid.NewString(), CaseID: outcome.CaseID, ActorID: "repair-runner",
		Type: "investigation_outcome", DetailsJSON: string(details), CreatedAt: outcome.RecordedAt.UTC()}
	if err := insertRepairEventTx(ctx, tx, event); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func insertRepairContentTx(ctx context.Context, tx *sql.Tx, attemptID, kind string, content []byte, now time.Time) error {
	digest := sha256.Sum256(content)
	_, err := tx.ExecContext(ctx, `INSERT INTO repair_artifacts
		(id,attempt_id,kind,content_sha256,artifact_ref,byte_size,created_at,content)
		VALUES ($1,$2,$3,$4,'database',$5,$6,$7)`, uuid.NewString(), attemptID, kind,
		hex.EncodeToString(digest[:]), len(content), now.UTC(), content)
	return err
}

func (s *PostgresStore) GetRepairArtifactContent(ctx context.Context, attemptID, kind string) ([]byte, error) {
	var content []byte
	var digest string
	err := s.db.QueryRowContext(ctx, `SELECT content,content_sha256 FROM repair_artifacts
		WHERE attempt_id=$1 AND kind=$2 AND artifact_ref='database'`, attemptID, kind).Scan(&content, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	actual := sha256.Sum256(content)
	if hex.EncodeToString(actual[:]) != digest {
		return nil, errors.New("repair artifact content digest mismatch")
	}
	return content, nil
}
