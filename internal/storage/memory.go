package storage

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/Cyaside/Triovexa/internal/domain"
)

type MemoryStore struct {
	mu         sync.RWMutex
	incidents  map[string]domain.Incident
	audit      map[string][]domain.AuditEvent
	evidence   map[string][]domain.EvidenceItem
	documents  map[string][]domain.DocumentReference
	actions    map[string][]domain.CandidateAction
	triageByID map[string]domain.TriageResult
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		incidents:  make(map[string]domain.Incident),
		audit:      make(map[string][]domain.AuditEvent),
		evidence:   make(map[string][]domain.EvidenceItem),
		documents:  make(map[string][]domain.DocumentReference),
		actions:    make(map[string][]domain.CandidateAction),
		triageByID: make(map[string]domain.TriageResult),
	}
}

func (s *MemoryStore) Close() error { return nil }

func (s *MemoryStore) CreateIncident(_ context.Context, incident domain.Incident) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.incidents[incident.ID] = incident
	return nil
}

func (s *MemoryStore) UpdateIncidentState(_ context.Context, incidentID string, state domain.IncidentState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	incident, ok := s.incidents[incidentID]
	if !ok {
		return ErrNotFound
	}
	incident.State = state
	incident.UpdatedAt = time.Now().UTC()
	s.incidents[incidentID] = incident
	return nil
}

func (s *MemoryStore) GetIncident(_ context.Context, incidentID string) (domain.Incident, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	incident, ok := s.incidents[incidentID]
	if !ok {
		return domain.Incident{}, ErrNotFound
	}
	return incident, nil
}

func (s *MemoryStore) ListIncidents(_ context.Context) ([]domain.Incident, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	incidents := make([]domain.Incident, 0, len(s.incidents))
	for _, incident := range s.incidents {
		incidents = append(incidents, incident)
	}
	sort.SliceStable(incidents, func(i, j int) bool {
		return incidents[i].CreatedAt.After(incidents[j].CreatedAt)
	})
	return incidents, nil
}

func (s *MemoryStore) AddAuditEvent(_ context.Context, event domain.AuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audit[event.IncidentID] = append(s.audit[event.IncidentID], event)
	return nil
}

func (s *MemoryStore) ListAuditEvents(_ context.Context, incidentID string) ([]domain.AuditEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	events := append([]domain.AuditEvent(nil), s.audit[incidentID]...)
	sort.SliceStable(events, func(i, j int) bool {
		return events[i].StartedAt.Before(events[j].StartedAt)
	})
	return events, nil
}

func (s *MemoryStore) SaveEvidenceItems(_ context.Context, items []domain.EvidenceItem) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range items {
		s.evidence[item.IncidentID] = append(s.evidence[item.IncidentID], item)
	}
	return nil
}

func (s *MemoryStore) ListEvidenceItems(_ context.Context, incidentID string) ([]domain.EvidenceItem, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := append([]domain.EvidenceItem(nil), s.evidence[incidentID]...)
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].Timestamp.Before(items[j].Timestamp)
	})
	return items, nil
}

func (s *MemoryStore) SaveDocumentReferences(_ context.Context, references []domain.DocumentReference) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, reference := range references {
		s.documents[reference.IncidentID] = append(s.documents[reference.IncidentID], reference)
	}
	return nil
}

func (s *MemoryStore) ListDocumentReferences(_ context.Context, incidentID string) ([]domain.DocumentReference, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	references := append([]domain.DocumentReference(nil), s.documents[incidentID]...)
	return references, nil
}

func (s *MemoryStore) SaveTriageResult(_ context.Context, result domain.TriageResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.triageByID[result.IncidentID] = result
	return nil
}

func (s *MemoryStore) GetTriageResult(_ context.Context, incidentID string) (domain.TriageResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result, ok := s.triageByID[incidentID]
	if !ok {
		return domain.TriageResult{}, ErrNotFound
	}
	return result, nil
}

func (s *MemoryStore) SaveCandidateActions(_ context.Context, actions []domain.CandidateAction) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	grouped := make(map[string][]domain.CandidateAction)
	for _, action := range actions {
		grouped[action.IncidentID] = append(grouped[action.IncidentID], action)
	}

	for incidentID, incidentActions := range grouped {
		s.actions[incidentID] = append([]domain.CandidateAction(nil), incidentActions...)
	}

	return nil
}

func (s *MemoryStore) ListCandidateActions(_ context.Context, incidentID string) ([]domain.CandidateAction, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	actions := append([]domain.CandidateAction(nil), s.actions[incidentID]...)
	sort.SliceStable(actions, func(i, j int) bool {
		return actions[i].CreatedAt.Before(actions[j].CreatedAt)
	})

	return actions, nil
}
