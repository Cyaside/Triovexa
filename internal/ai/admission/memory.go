package admission

import (
	"bytes"
	"context"
	"sync"
	"time"
)

// MemoryStore is an explicit fixture store. Applications with paid model calls
// must use a durable Store; this store cannot survive a process restart.
type MemoryStore struct {
	mu        sync.Mutex
	campaigns map[string]Campaign
	requests  map[string]Reservation
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{campaigns: map[string]Campaign{}, requests: map[string]Reservation{}}
}
func (m *MemoryStore) CreateCampaign(_ context.Context, c Campaign) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if previous, ok := m.campaigns[c.ID]; ok {
		if previous.Profile != c.Profile || previous.Offline != c.Offline || previous.MaxSpendMicroUSD != c.MaxSpendMicroUSD || previous.MaxInputTokens != c.MaxInputTokens || previous.MaxRequests != c.MaxRequests {
			return ErrMismatch
		}
		return nil
	}
	m.campaigns[c.ID] = c
	return nil
}
func (m *MemoryStore) GetCampaign(_ context.Context, id string) (Campaign, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.campaigns[id]
	if !ok {
		return c, ErrNotFound
	}
	return c, nil
}
func (m *MemoryStore) GetRequest(_ context.Context, id string) (Reservation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.requests[id]
	if !ok {
		return r, ErrNotFound
	}
	return cloneReservation(r), nil
}
func (m *MemoryStore) Reserve(_ context.Context, r Reservation) (Reservation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.campaigns[r.Request.CampaignID]
	if !ok {
		return r, ErrNotFound
	}
	if previous, ok := m.requests[r.Request.ID]; ok {
		if previous.Request != r.Request || previous.Pricing != r.Pricing || previous.ReservedMicroUSD != r.ReservedMicroUSD {
			return r, ErrMismatch
		}
		if previous.State == Dispatching || previous.State == Uncertain {
			return cloneReservation(previous), ErrUncertain
		}
		if previous.State == Cancelled {
			return cloneReservation(previous), ErrAlreadyDispatched
		}
		return cloneReservation(previous), nil
	}
	if c.Offline && r.Request.External {
		return r, ErrOffline
	}
	if c.Blocked || m.pending(c.ID) {
		return r, ErrUncertain
	}
	if c.Requests >= c.MaxRequests || r.Request.InputTokenBound > c.MaxInputTokens-c.AdmittedInputTokens || r.ReservedMicroUSD > c.MaxSpendMicroUSD-c.SpentMicroUSD-c.ReservedMicroUSD {
		return r, ErrBudgetExceeded
	}
	c.ReservedMicroUSD += r.ReservedMicroUSD
	c.AdmittedInputTokens += r.Request.InputTokenBound
	c.Requests++
	m.campaigns[c.ID] = c
	m.requests[r.Request.ID] = r
	return cloneReservation(r), nil
}
func (m *MemoryStore) pending(id string) bool {
	for _, r := range m.requests {
		if r.Request.CampaignID == id && r.Request.External && (r.State == Dispatching || r.State == Uncertain) {
			return true
		}
	}
	return false
}
func (m *MemoryStore) lookup(id, hash string) (Campaign, Reservation, error) {
	r, ok := m.requests[id]
	if !ok {
		return Campaign{}, r, ErrNotFound
	}
	if r.Request.PayloadHash != hash {
		return Campaign{}, r, ErrMismatch
	}
	return m.campaigns[r.Request.CampaignID], r, nil
}
func (m *MemoryStore) StartDispatch(_ context.Context, id, hash string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, r, err := m.lookup(id, hash)
	if err != nil {
		return err
	}
	if r.State != Reserved {
		return ErrAlreadyDispatched
	}
	if c.Blocked {
		return ErrUncertain
	}
	if r.Request.External {
		if c.Offline {
			return ErrOffline
		}
		if m.pending(c.ID) {
			return ErrUncertain
		}
	}
	r.State = Dispatching
	r.DispatchedAt = now
	m.requests[id] = r
	return nil
}
func (m *MemoryStore) RecordResponse(_ context.Context, receipt Receipt, cost int64, valid bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, r, err := m.lookup(receipt.RequestID, receipt.PayloadHash)
	if err != nil {
		return err
	}
	if r.Receipt != nil {
		if r.Receipt.Usage != receipt.Usage || r.Receipt.HTTPStatus != receipt.HTTPStatus || r.Receipt.ResponseEncoding != receipt.ResponseEncoding || !bytes.Equal(r.Receipt.Response, receipt.Response) {
			return ErrMismatch
		}
		return nil
	}
	if r.State != Dispatching && r.State != Uncertain {
		return ErrAlreadyDispatched
	}
	if cost < 0 || cost > r.ReservedMicroUSD {
		return ErrInvalid
	}
	r.Receipt = &receipt
	r.ActualMicroUSD = cost
	r.FinishedAt = receipt.RecordedAt
	r.State = Accounted
	if valid {
		c.ReservedMicroUSD -= r.ReservedMicroUSD
		c.SpentMicroUSD += cost
	} else {
		r.State = Uncertain
		c.Blocked = true
	}
	m.requests[receipt.RequestID] = cloneReservation(r)
	m.campaigns[c.ID] = c
	return nil
}
func (m *MemoryStore) MarkUncertain(_ context.Context, id, hash string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, r, err := m.lookup(id, hash)
	if err != nil {
		return err
	}
	if r.State == Uncertain {
		return nil
	}
	if r.State != Dispatching {
		return ErrAlreadyDispatched
	}
	r.State = Uncertain
	r.FinishedAt = now
	c.Blocked = true
	m.requests[id] = r
	m.campaigns[c.ID] = c
	return nil
}
func (m *MemoryStore) CancelReserved(_ context.Context, id, hash string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, r, err := m.lookup(id, hash)
	if err != nil {
		return err
	}
	if r.State == Cancelled {
		return nil
	}
	if r.State != Reserved {
		return ErrAlreadyDispatched
	}
	c.ReservedMicroUSD -= r.ReservedMicroUSD
	c.AdmittedInputTokens -= r.Request.InputTokenBound
	c.Requests--
	r.State = Cancelled
	r.FinishedAt = now
	m.requests[id] = r
	m.campaigns[c.ID] = c
	return nil
}
func cloneReservation(r Reservation) Reservation {
	if r.Receipt != nil {
		receipt := *r.Receipt
		receipt.Response = bytes.Clone(receipt.Response)
		r.Receipt = &receipt
	}
	return r
}
