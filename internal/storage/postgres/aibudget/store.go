// Package aibudget persists shared model reservations and private response receipts.
package aibudget

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/Cyaside/Triovexa/internal/ai/admission"
)

type Store struct{ db *sql.DB }

func New(db *sql.DB) *Store { return &Store{db: db} }

var _ admission.Store = (*Store)(nil)

const campaignColumns = `id,profile,offline,max_spend_micro_usd,max_input_tokens,max_requests,
 spent_micro_usd,reserved_micro_usd,admitted_input_tokens,requests,blocked,created_at,provider_managed`
const requestColumns = `identity_json::text,pricing_json::text,reserved_micro_usd,actual_micro_usd,
 state,receipt_json::text,response,response_sha256,created_at,dispatched_at,finished_at`

type scanner interface{ Scan(...any) error }

func scanCampaign(row scanner) (admission.Campaign, error) {
	var c admission.Campaign
	err := row.Scan(&c.ID, &c.Profile, &c.Offline, &c.MaxSpendMicroUSD, &c.MaxInputTokens, &c.MaxRequests,
		&c.SpentMicroUSD, &c.ReservedMicroUSD, &c.AdmittedInputTokens, &c.Requests, &c.Blocked, &c.CreatedAt, &c.ProviderManaged)
	if errors.Is(err, sql.ErrNoRows) {
		return c, admission.ErrNotFound
	}
	return c, err
}
func scanRequest(row scanner) (admission.Reservation, error) {
	var r admission.Reservation
	var identity, pricing string
	var receipt, digest sql.NullString
	var response []byte
	var dispatch, finish sql.NullTime
	err := row.Scan(&identity, &pricing, &r.ReservedMicroUSD, &r.ActualMicroUSD, &r.State, &receipt, &response, &digest, &r.CreatedAt, &dispatch, &finish)
	if errors.Is(err, sql.ErrNoRows) {
		return r, admission.ErrNotFound
	}
	if err != nil {
		return r, err
	}
	if json.Unmarshal([]byte(identity), &r.Request) != nil || json.Unmarshal([]byte(pricing), &r.Pricing) != nil {
		return r, admission.ErrInvalid
	}
	if receipt.Valid {
		r.Receipt = &admission.Receipt{}
		if json.Unmarshal([]byte(receipt.String), r.Receipt) != nil {
			return r, admission.ErrInvalid
		}
		if len(response) > 0 && (!digest.Valid || admission.PayloadHash(response) != digest.String) {
			return r, admission.ErrMismatch
		}
		r.Receipt.Response = response
	}
	if dispatch.Valid {
		r.DispatchedAt = dispatch.Time
	}
	if finish.Valid {
		r.FinishedAt = finish.Time
	}
	return r, nil
}
func (s *Store) GetCampaign(ctx context.Context, id string) (admission.Campaign, error) {
	return scanCampaign(s.db.QueryRowContext(ctx, `SELECT `+campaignColumns+` FROM ai_budget_campaigns WHERE id=$1`, id))
}
func (s *Store) GetRequest(ctx context.Context, id string) (admission.Reservation, error) {
	return scanRequest(s.db.QueryRowContext(ctx, `SELECT `+requestColumns+` FROM ai_model_requests WHERE id=$1`, id))
}

func (s *Store) CreateCampaign(ctx context.Context, c admission.Campaign) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO ai_budget_campaigns(id,profile,offline,max_spend_micro_usd,max_input_tokens,max_requests,created_at,provider_managed)
	VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(id) DO NOTHING`, c.ID, c.Profile, c.Offline, c.MaxSpendMicroUSD, c.MaxInputTokens, c.MaxRequests, c.CreatedAt, c.ProviderManaged)
	if err != nil {
		return err
	}
	stored, err := s.GetCampaign(ctx, c.ID)
	if err != nil {
		return err
	}
	if stored.Profile != c.Profile || stored.Offline != c.Offline || stored.ProviderManaged != c.ProviderManaged || stored.MaxSpendMicroUSD != c.MaxSpendMicroUSD || stored.MaxInputTokens != c.MaxInputTokens || stored.MaxRequests != c.MaxRequests {
		return admission.ErrMismatch
	}
	return nil
}

func (s *Store) Reserve(ctx context.Context, r admission.Reservation) (admission.Reservation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return r, err
	}
	defer tx.Rollback()
	c, err := scanCampaign(tx.QueryRowContext(ctx, `SELECT `+campaignColumns+` FROM ai_budget_campaigns WHERE id=$1 FOR UPDATE`, r.Request.CampaignID))
	if err != nil {
		return r, err
	}
	previous, err := scanRequest(tx.QueryRowContext(ctx, `SELECT `+requestColumns+` FROM ai_model_requests WHERE id=$1`, r.Request.ID))
	if err == nil {
		if previous.Request != r.Request || previous.Pricing != r.Pricing || previous.ReservedMicroUSD != r.ReservedMicroUSD {
			return r, admission.ErrMismatch
		}
		if previous.State == admission.Dispatching || previous.State == admission.Uncertain {
			return previous, admission.ErrUncertain
		}
		if previous.State == admission.Cancelled {
			return previous, admission.ErrAlreadyDispatched
		}
		return previous, tx.Commit()
	}
	if !errors.Is(err, admission.ErrNotFound) {
		return r, err
	}
	if r.Request.External && c.Offline {
		return r, admission.ErrOffline
	}
	if c.Blocked {
		return r, admission.ErrUncertain
	}
	var pending bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM ai_model_requests WHERE campaign_id=$1 AND external AND state IN ('dispatching','uncertain'))`, c.ID).Scan(&pending); err != nil {
		return r, err
	}
	if pending {
		return r, admission.ErrUncertain
	}
	if c.ExceedsLimits(r) {
		return r, admission.ErrBudgetExceeded
	}
	identity, _ := json.Marshal(r.Request)
	pricing, _ := json.Marshal(r.Pricing)
	result, err := tx.ExecContext(ctx, `INSERT INTO ai_model_requests(id,campaign_id,identity_json,pricing_json,payload_hash,external,input_token_bound,reserved_micro_usd,state,created_at)
	VALUES($1,$2,$3,$4,$5,$6,$7,$8,'reserved',$9) ON CONFLICT(id) DO NOTHING`, r.Request.ID, c.ID, string(identity), string(pricing), r.Request.PayloadHash, r.Request.External, r.Request.InputTokenBound, r.ReservedMicroUSD, r.CreatedAt)
	if err != nil {
		return r, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return r, err
	}
	if changed != 1 {
		return r, admission.ErrMismatch
	}
	_, err = tx.ExecContext(ctx, `UPDATE ai_budget_campaigns SET reserved_micro_usd=reserved_micro_usd+$1,admitted_input_tokens=admitted_input_tokens+$2,requests=requests+1 WHERE id=$3`, r.ReservedMicroUSD, r.Request.InputTokenBound, c.ID)
	if err != nil {
		return r, err
	}
	return r, tx.Commit()
}

// requestTx always locks campaign before request, matching Reserve and avoiding
// deadlocks between accounting, concurrent reservations and cancellation.
func (s *Store) requestTx(ctx context.Context, id, hash string) (*sql.Tx, admission.Campaign, admission.Reservation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, admission.Campaign{}, admission.Reservation{}, err
	}
	fail := func(err error) (*sql.Tx, admission.Campaign, admission.Reservation, error) {
		_ = tx.Rollback()
		return nil, admission.Campaign{}, admission.Reservation{}, err
	}
	var campaignID string
	err = tx.QueryRowContext(ctx, `SELECT campaign_id FROM ai_model_requests WHERE id=$1`, id).Scan(&campaignID)
	if errors.Is(err, sql.ErrNoRows) {
		return fail(admission.ErrNotFound)
	}
	if err != nil {
		return fail(err)
	}
	c, err := scanCampaign(tx.QueryRowContext(ctx, `SELECT `+campaignColumns+` FROM ai_budget_campaigns WHERE id=$1 FOR UPDATE`, campaignID))
	if err != nil {
		return fail(err)
	}
	r, err := scanRequest(tx.QueryRowContext(ctx, `SELECT `+requestColumns+` FROM ai_model_requests WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return fail(err)
	}
	if r.Request.PayloadHash != hash {
		return fail(admission.ErrMismatch)
	}
	return tx, c, r, nil
}
func (s *Store) StartDispatch(ctx context.Context, id, hash string, now time.Time) error {
	tx, c, r, err := s.requestTx(ctx, id, hash)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if r.State != admission.Reserved {
		return admission.ErrAlreadyDispatched
	}
	if c.Blocked {
		return admission.ErrUncertain
	}
	if r.Request.External {
		if c.Offline {
			return admission.ErrOffline
		}
		var pending bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM ai_model_requests WHERE campaign_id=$1 AND external AND state IN ('dispatching','uncertain'))`, c.ID).Scan(&pending); err != nil {
			return err
		}
		if pending {
			return admission.ErrUncertain
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE ai_model_requests SET state='dispatching',dispatched_at=$1 WHERE id=$2`, now, id)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) RecordResponse(ctx context.Context, receipt admission.Receipt, cost int64, valid bool) error {
	if len(receipt.Response) > 0 && receipt.ResponseEncoding != "sealed-v1" {
		return admission.ErrInvalid
	}
	tx, c, r, err := s.requestTx(ctx, receipt.RequestID, receipt.PayloadHash)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if r.Receipt != nil {
		if r.Receipt.Usage != receipt.Usage || r.Receipt.HTTPStatus != receipt.HTTPStatus || r.Receipt.ResponseEncoding != receipt.ResponseEncoding || !bytes.Equal(r.Receipt.Response, receipt.Response) {
			return admission.ErrMismatch
		}
		return tx.Commit()
	}
	if r.State != admission.Dispatching && r.State != admission.Uncertain {
		return admission.ErrAlreadyDispatched
	}
	if cost < 0 || cost > r.ReservedMicroUSD {
		return admission.ErrInvalid
	}
	if valid && (!admission.ValidUsage(receipt.Usage) || receipt.Usage.PromptTokens > r.Request.InputTokenBound || receipt.Usage.CompletionTokens > r.Request.OutputTokenBound) {
		return admission.ErrUsageInvalid
	}
	response := receipt.Response
	receipt.Response = nil
	metadata, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	next := admission.Accounted
	if !valid {
		next = admission.Uncertain
	}
	_, err = tx.ExecContext(ctx, `UPDATE ai_model_requests SET state=$1,actual_micro_usd=$2,receipt_json=$3,response=$4,response_sha256=$5,finished_at=$6 WHERE id=$7`, next, cost, string(metadata), response, admission.PayloadHash(response), receipt.RecordedAt, receipt.RequestID)
	if err != nil {
		return err
	}
	if valid {
		_, err = tx.ExecContext(ctx, `UPDATE ai_budget_campaigns SET reserved_micro_usd=reserved_micro_usd-$1,spent_micro_usd=spent_micro_usd+$2,
		admitted_input_tokens=admitted_input_tokens-$3 WHERE id=$4`, r.ReservedMicroUSD, cost, r.Request.InputTokenBound-receipt.Usage.PromptTokens, c.ID)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE ai_budget_campaigns SET blocked=true WHERE id=$1`, c.ID)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) MarkUncertain(ctx context.Context, id, hash string, now time.Time) error {
	tx, c, r, err := s.requestTx(ctx, id, hash)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if r.State == admission.Uncertain {
		return tx.Commit()
	}
	if r.State != admission.Dispatching {
		return admission.ErrAlreadyDispatched
	}
	if _, err = tx.ExecContext(ctx, `UPDATE ai_model_requests SET state='uncertain',finished_at=$1 WHERE id=$2`, now, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE ai_budget_campaigns SET blocked=true WHERE id=$1`, c.ID); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) CancelReserved(ctx context.Context, id, hash string, now time.Time) error {
	tx, c, r, err := s.requestTx(ctx, id, hash)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if r.State == admission.Cancelled {
		return tx.Commit()
	}
	if r.State != admission.Reserved {
		return admission.ErrAlreadyDispatched
	}
	if _, err = tx.ExecContext(ctx, `UPDATE ai_model_requests SET state='cancelled',finished_at=$1 WHERE id=$2`, now, id); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE ai_budget_campaigns SET reserved_micro_usd=reserved_micro_usd-$1,admitted_input_tokens=admitted_input_tokens-$2,requests=requests-1 WHERE id=$3`, r.ReservedMicroUSD, r.Request.InputTokenBound, c.ID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
