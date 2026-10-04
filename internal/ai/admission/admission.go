// Package admission owns model dispatch admission across components and attempts.
// Campaign identifiers are supplied by the trusted application, never the model.
package admission

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"

	"github.com/Cyaside/Triovexa/internal/security"
)

var (
	ErrInvalid           = errors.New("invalid admission request")
	ErrNotFound          = errors.New("budget record not found")
	ErrMismatch          = errors.New("immutable admission identity mismatch")
	ErrOffline           = errors.New("offline profile rejects external model dispatch")
	ErrPricingUnknown    = errors.New("pricing_unknown")
	ErrBillingUnbounded  = errors.New("billing_unbounded")
	ErrBudgetExceeded    = errors.New("budget_exhausted")
	ErrUncertain         = errors.New("dispatch_uncertain")
	ErrAlreadyDispatched = errors.New("model request already dispatched")
	ErrUsageInvalid      = errors.New("usage_invalid")
)

type State string

const (
	Reserved    State = "reserved"
	Dispatching State = "dispatching"
	Accounted   State = "accounted"
	Uncertain   State = "uncertain"
	Cancelled   State = "cancelled"
)

type Campaign struct {
	ID                  string    `json:"id"`
	Profile             string    `json:"profile"`
	Offline             bool      `json:"offline"`
	MaxSpendMicroUSD    int64     `json:"max_spend_micro_usd"`
	MaxInputTokens      int64     `json:"max_input_tokens"`
	MaxRequests         int64     `json:"max_requests"`
	SpentMicroUSD       int64     `json:"spent_micro_usd"`
	ReservedMicroUSD    int64     `json:"reserved_micro_usd"`
	AdmittedInputTokens int64     `json:"admitted_input_tokens"`
	Requests            int64     `json:"requests"`
	Blocked             bool      `json:"blocked"`
	CreatedAt           time.Time `json:"created_at"`
}

type Pricing struct {
	Version  string `json:"version"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Verified bool   `json:"verified"`
	// InputBoundVerified asserts the configured tokenizer/billing contract
	// guarantees the caller's upper bound. A bytes/4 estimate never qualifies.
	InputBoundVerified       bool   `json:"input_bound_verified"`
	InputContract            string `json:"input_contract,omitempty"`
	BillableOutputBound      bool   `json:"billable_output_bound"`
	InputMicroUSDPerMillion  int64  `json:"input_micro_usd_per_million"`
	OutputMicroUSDPerMillion int64  `json:"output_micro_usd_per_million"`
	FixedRequestMicroUSD     int64  `json:"fixed_request_micro_usd"`
}

type Request struct {
	ID               string `json:"id"`
	CampaignID       string `json:"campaign_id"`
	AttemptID        string `json:"attempt_id"`
	Phase            string `json:"phase"`
	ConfigVersion    string `json:"config_version"`
	PayloadHash      string `json:"payload_hash"`
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	External         bool   `json:"external"`
	InputTokenBound  int64  `json:"input_token_bound"`
	OutputTokenBound int64  `json:"output_token_bound"`
}

type Usage struct {
	Present          bool  `json:"present"`
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
	// Detail counts are subsets; they are not added again to billable totals.
	CachedInputTokens int64 `json:"cached_input_tokens"`
	ReasoningTokens   int64 `json:"reasoning_tokens"`
}

type Receipt struct {
	RequestID   string `json:"request_id"`
	PayloadHash string `json:"payload_hash"`
	Usage       Usage  `json:"usage"`
	HTTPStatus  int    `json:"http_status"`
	LatencyMS   int64  `json:"latency_ms"`
	// Response is an opaque private receipt, sealed by a persistent gateway.
	// It must never be exposed in a public ledger/status API.
	Response         []byte    `json:"response,omitempty"`
	ResponseEncoding string    `json:"response_encoding"`
	RecordedAt       time.Time `json:"recorded_at"`
}

type Reservation struct {
	Request          Request   `json:"request"`
	Pricing          Pricing   `json:"pricing"`
	ReservedMicroUSD int64     `json:"reserved_micro_usd"`
	ActualMicroUSD   int64     `json:"actual_micro_usd"`
	State            State     `json:"state"`
	Receipt          *Receipt  `json:"receipt,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	DispatchedAt     time.Time `json:"dispatched_at"`
	FinishedAt       time.Time `json:"finished_at"`
}

// Store performs each campaign/request change atomically. Durable adapters
// retain dispatch markers and ambiguous reservations after process restart.
type Store interface {
	CreateCampaign(context.Context, Campaign) error
	GetCampaign(context.Context, string) (Campaign, error)
	Reserve(context.Context, Reservation) (Reservation, error)
	StartDispatch(context.Context, string, string, time.Time) error
	RecordResponse(context.Context, Receipt, int64, bool) error
	MarkUncertain(context.Context, string, string, time.Time) error
	CancelReserved(context.Context, string, string, time.Time) error
	GetRequest(context.Context, string) (Reservation, error)
}

type Service struct{ store Store }

func NewService(store Store) (*Service, error) {
	if store == nil {
		return nil, ErrInvalid
	}
	return &Service{store: store}, nil
}

func (s *Service) CreateCampaign(ctx context.Context, c Campaign) error {
	if !identifier(c.ID) || !identifier(c.Profile) || c.MaxSpendMicroUSD < 0 ||
		c.MaxInputTokens <= 0 || c.MaxRequests <= 0 || c.SpentMicroUSD != 0 ||
		c.ReservedMicroUSD != 0 || c.AdmittedInputTokens != 0 || c.Requests != 0 || c.Blocked {
		return ErrInvalid
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	return s.store.CreateCampaign(ctx, c)
}
func (s *Service) GetCampaign(ctx context.Context, id string) (Campaign, error) {
	return s.store.GetCampaign(ctx, id)
}
func (s *Service) GetRequest(ctx context.Context, id string) (Reservation, error) {
	return s.store.GetRequest(ctx, id)
}

func (s *Service) Reserve(ctx context.Context, r Request, p Pricing) (Reservation, error) {
	if !identifier(r.ID) || !identifier(r.CampaignID) || !identifier(r.AttemptID) || !identifier(r.Phase) ||
		!identifier(r.ConfigVersion) || !identifier(r.Provider) || !identifier(r.Model) || !validHash(r.PayloadHash) ||
		r.InputTokenBound <= 0 || r.OutputTokenBound <= 0 || !identifier(p.Version) || p.Provider != r.Provider || p.Model != r.Model ||
		p.InputMicroUSDPerMillion < 0 || p.OutputMicroUSDPerMillion < 0 || p.FixedRequestMicroUSD < 0 {
		return Reservation{}, ErrInvalid
	}
	c, err := s.store.GetCampaign(ctx, r.CampaignID)
	if err != nil {
		return Reservation{}, err
	}
	if r.External && c.Offline {
		return Reservation{}, ErrOffline
	}
	if r.External && !p.Verified {
		return Reservation{}, ErrPricingUnknown
	}
	if r.External && (!p.InputBoundVerified || !p.BillableOutputBound) {
		return Reservation{}, ErrBillingUnbounded
	}
	cost, err := Cost(p, r.InputTokenBound, r.OutputTokenBound)
	if err != nil {
		return Reservation{}, err
	}
	return s.store.Reserve(ctx, Reservation{Request: r, Pricing: p, ReservedMicroUSD: cost, State: Reserved, CreatedAt: time.Now().UTC()})
}
func (s *Service) StartDispatch(ctx context.Context, id, hash string) error {
	return s.store.StartDispatch(ctx, id, hash, time.Now().UTC())
}
func (s *Service) MarkUncertain(ctx context.Context, id, hash string) error {
	return s.store.MarkUncertain(ctx, id, hash, time.Now().UTC())
}
func (s *Service) CancelReserved(ctx context.Context, id, hash string) error {
	return s.store.CancelReserved(ctx, id, hash, time.Now().UTC())
}

func (s *Service) RecordResponse(ctx context.Context, r Receipt) error {
	if !identifier(r.RequestID) || !validHash(r.PayloadHash) || r.HTTPStatus < 100 || r.HTTPStatus > 599 ||
		r.LatencyMS < 0 || len(r.Response) > 256*1024 || !identifier(r.ResponseEncoding) {
		return ErrInvalid
	}
	stored, err := s.store.GetRequest(ctx, r.RequestID)
	if err != nil {
		return err
	}
	if stored.Request.PayloadHash != r.PayloadHash {
		return ErrMismatch
	}
	if r.RecordedAt.IsZero() {
		r.RecordedAt = time.Now().UTC()
	}
	valid := ValidUsage(r.Usage) && r.Usage.PromptTokens <= stored.Request.InputTokenBound && r.Usage.CompletionTokens <= stored.Request.OutputTokenBound
	cost := int64(0)
	if valid {
		cost, err = Cost(stored.Pricing, r.Usage.PromptTokens, r.Usage.CompletionTokens)
		if err != nil {
			return err
		}
		valid = cost <= stored.ReservedMicroUSD
	}
	if err = s.store.RecordResponse(ctx, r, cost, valid); err != nil {
		return err
	}
	if !valid {
		return ErrUsageInvalid
	}
	return nil
}

func ValidUsage(u Usage) bool {
	return u.Present && u.PromptTokens >= 0 && u.CompletionTokens >= 0 && u.TotalTokens >= 0 &&
		u.PromptTokens <= math.MaxInt64-u.CompletionTokens && u.TotalTokens == u.PromptTokens+u.CompletionTokens &&
		u.CachedInputTokens >= 0 && u.CachedInputTokens <= u.PromptTokens && u.ReasoningTokens >= 0 && u.ReasoningTokens <= u.CompletionTokens
}

// Cost uses conservative uncached rates and rounds up to a whole micro-USD.
// It rejects arithmetic overflow instead of wrapping a hard ceiling.
func Cost(p Pricing, input, output int64) (int64, error) {
	if input < 0 || output < 0 || p.InputMicroUSDPerMillion < 0 || p.OutputMicroUSDPerMillion < 0 || p.FixedRequestMicroUSD < 0 {
		return 0, ErrInvalid
	}
	in, err := rateCost(input, p.InputMicroUSDPerMillion)
	if err != nil {
		return 0, err
	}
	out, err := rateCost(output, p.OutputMicroUSDPerMillion)
	if err != nil {
		return 0, err
	}
	if in > math.MaxInt64-out || in+out > math.MaxInt64-p.FixedRequestMicroUSD {
		return 0, fmt.Errorf("%w: cost overflow", ErrInvalid)
	}
	return in + out + p.FixedRequestMicroUSD, nil
}
func rateCost(tokens, rate int64) (int64, error) {
	if rate != 0 && tokens > math.MaxInt64/rate {
		return 0, fmt.Errorf("%w: cost overflow", ErrInvalid)
	}
	n := tokens * rate
	q := n / 1_000_000
	if n%1_000_000 != 0 {
		q++
	}
	return q, nil
}

func PayloadHash(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// ConservativeInputBound counts every UTF-8 payload byte plus explicitly
// verified protocol overhead. This is an upper bound only for a tokenizer
// contract verified by the caller; arbitrary model tokenizers are unsupported.
func ConservativeInputBound(payload []byte, overhead int64) (int64, error) {
	if len(payload) == 0 || overhead < 0 || int64(len(payload)) > math.MaxInt64-overhead {
		return 0, ErrInvalid
	}
	return int64(len(payload)) + overhead, nil
}
func identifier(s string) bool {
	return s != "" && len(s) <= 256 && strings.TrimSpace(s) == s && strings.IndexFunc(s, unicode.IsControl) < 0 && security.Redact(s) == s
}
func validHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && strings.ToLower(s) == s
}
