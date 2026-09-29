package repair

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"

	"github.com/Cyaside/Triovexa/internal/coderepair"
)

type RepairPREvent = coderepair.PREvent

// ApplyRepairPREvent accepts only an authenticated webhook whose immutable
// branch, PR number and head still match the recorded publication. Delivery
// identity and state change are committed together; duplicates are no-ops.
func (s *Store) ApplyRepairPREvent(ctx context.Context, event RepairPREvent) (bool, error) {
	if _, err := uuid.Parse(event.DeliveryID); err != nil || event.Branch == "" || event.Number < 1 ||
		!coderepair.ValidGitRevision(event.HeadSHA) || event.ReceivedAt.IsZero() ||
		(event.Merged && !coderepair.ValidGitRevision(event.MergeSHA)) {
		return false, errors.New("invalid repair PR delivery")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	p, err := scanPublication(tx.QueryRowContext(ctx, `SELECT `+publicationColumns+`
		FROM repair_publications WHERE branch_name=$1 FOR UPDATE`, event.Branch))
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if p.State != coderepair.PublicationPROpen || p.PRNumber != event.Number || p.HeadSHA != event.HeadSHA {
		return false, nil
	}
	c, err := scanRepairCase(tx.QueryRowContext(ctx, `SELECT `+repairCaseColumns+` FROM repair_cases WHERE id=$1 FOR UPDATE`, p.CaseID))
	if err != nil {
		return false, err
	}
	if c.State != coderepair.StatePROpen {
		return false, nil
	}
	binding, err := getRepairBindingTx(ctx, tx, c.BindingID)
	if err != nil {
		return false, err
	}
	repo := strings.TrimPrefix(binding.RepositoryURL, "https://github.com/")
	repo = strings.TrimSuffix(repo, ".git")
	if !strings.EqualFold(event.Repository, repo) || event.BaseRef != binding.BaseRef {
		return false, nil
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO repair_github_deliveries
		(delivery_id,publication_id,event_type,action,received_at)
		VALUES ($1,$2,'pull_request','closed',$3) ON CONFLICT (delivery_id) DO NOTHING`,
		event.DeliveryID, p.ID, event.ReceivedAt.UTC())
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil || rows == 0 {
		return false, err
	}
	next := coderepair.StateClosedWithoutMerge
	pState := coderepair.PublicationClosed
	if event.Merged {
		next, pState = coderepair.StateMerged, coderepair.PublicationMerged
	}
	if _, err := tx.ExecContext(ctx, `UPDATE repair_publications SET state=$1,merge_sha=$2,updated_at=$3 WHERE id=$4`,
		pState, event.MergeSHA, event.ReceivedAt.UTC(), p.ID); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE repair_cases SET state=$1,version=version+1,updated_at=$2 WHERE id=$3`,
		next, event.ReceivedAt.UTC(), p.CaseID); err != nil {
		return false, err
	}
	details, _ := json.Marshal(map[string]any{"pr_number": p.PRNumber, "head_sha": p.HeadSHA,
		"merge_sha": event.MergeSHA, "delivery_id": event.DeliveryID})
	if err := insertRepairEventTx(ctx, tx, coderepair.Event{ID: uuid.NewString(), CaseID: p.CaseID,
		ActorID: "github-webhook", Type: "pull_request_closed", DetailsJSON: string(details), CreatedAt: event.ReceivedAt.UTC()}); err != nil {
		return false, err
	}
	return true, tx.Commit()
}
