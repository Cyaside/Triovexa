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
	policies   map[string]domain.PolicyDecision
	approvals  map[string][]domain.ApprovalRecord
	executions map[string][]domain.ExecutionRecord
	verify     map[string][]domain.VerificationResult
	triageByID map[string]domain.TriageResult
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		incidents:  make(map[string]domain.Incident),
		audit:      make(map[string][]domain.AuditEvent),
		evidence:   make(map[string][]domain.EvidenceItem),
		documents:  make(map[string][]domain.DocumentReference),
		actions:    make(map[string][]domain.CandidateAction),
		policies:   make(map[string]domain.PolicyDecision),
		approvals:  make(map[string][]domain.ApprovalRecord),
		executions: make(map[string][]domain.ExecutionRecord),
		verify:     make(map[string][]domain.VerificationResult),
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

func (s *MemoryStore) GetCandidateAction(_ context.Context, actionID string) (domain.CandidateAction, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, actions := range s.actions {
		for _, action := range actions {
			if action.ID == actionID {
				return action, nil
			}
		}
	}

	return domain.CandidateAction{}, ErrNotFound
}

func (s *MemoryStore) UpdateCandidateActionStatus(_ context.Context, actionID string, status domain.CandidateActionStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for incidentID, actions := range s.actions {
		for idx, action := range actions {
			if action.ID == actionID {
				action.Status = status
				s.actions[incidentID][idx] = action
				return nil
			}
		}
	}

	return ErrNotFound
}

func (s *MemoryStore) SavePolicyDecisions(_ context.Context, decisions []domain.PolicyDecision) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, decision := range decisions {
		s.policies[decision.CandidateActionID] = decision
	}

	return nil
}

func (s *MemoryStore) GetPolicyDecision(_ context.Context, actionID string) (domain.PolicyDecision, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	decision, ok := s.policies[actionID]
	if !ok {
		return domain.PolicyDecision{}, ErrNotFound
	}

	return decision, nil
}

func (s *MemoryStore) ListPolicyDecisions(_ context.Context, incidentID string) ([]domain.PolicyDecision, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var decisions []domain.PolicyDecision
	for _, action := range s.actions[incidentID] {
		if decision, ok := s.policies[action.ID]; ok {
			decisions = append(decisions, decision)
		}
	}

	sort.SliceStable(decisions, func(i, j int) bool {
		return decisions[i].DecidedAt.Before(decisions[j].DecidedAt)
	})

	return decisions, nil
}

func (s *MemoryStore) CreateApprovalRecord(_ context.Context, record domain.ApprovalRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.approvals[record.CandidateActionID] = append(s.approvals[record.CandidateActionID], record)
	return nil
}

func (s *MemoryStore) ListApprovalRecords(_ context.Context, incidentID string) ([]domain.ApprovalRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var records []domain.ApprovalRecord
	for _, action := range s.actions[incidentID] {
		records = append(records, s.approvals[action.ID]...)
	}

	sort.SliceStable(records, func(i, j int) bool {
		return records[i].CreatedAt.Before(records[j].CreatedAt)
	})

	return records, nil
}

func (s *MemoryStore) SaveExecutionRecord(_ context.Context, record domain.ExecutionRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	incidentID := ""
	for candidateIncidentID, actions := range s.actions {
		for _, action := range actions {
			if action.ID == record.CandidateActionID {
				incidentID = candidateIncidentID
				break
			}
		}
		if incidentID != "" {
			break
		}
	}

	if incidentID == "" {
		return ErrNotFound
	}

	records := s.executions[incidentID]
	for idx, existing := range records {
		if existing.ID == record.ID {
			records[idx] = record
			s.executions[incidentID] = records
			return nil
		}
	}

	s.executions[incidentID] = append(records, record)
	return nil
}

func (s *MemoryStore) ListExecutionRecords(_ context.Context, incidentID string) ([]domain.ExecutionRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	records := append([]domain.ExecutionRecord(nil), s.executions[incidentID]...)
	sort.SliceStable(records, func(i, j int) bool {
		return records[i].StartedAt.Before(records[j].StartedAt)
	})

	return records, nil
}

func (s *MemoryStore) ListExecutionRecordsByAction(_ context.Context, actionID string) ([]domain.ExecutionRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var records []domain.ExecutionRecord
	for _, incidentRecords := range s.executions {
		for _, record := range incidentRecords {
			if record.CandidateActionID == actionID {
				records = append(records, record)
			}
		}
	}

	sort.SliceStable(records, func(i, j int) bool {
		return records[i].StartedAt.Before(records[j].StartedAt)
	})

	return records, nil
}

func (s *MemoryStore) SaveVerificationResult(_ context.Context, result domain.VerificationResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	incidentID := ""
	for candidateIncidentID, records := range s.executions {
		for _, record := range records {
			if record.ID == result.ExecutionRecordID {
				incidentID = candidateIncidentID
				break
			}
		}
		if incidentID != "" {
			break
		}
	}

	if incidentID == "" {
		return ErrNotFound
	}

	results := s.verify[incidentID]
	for idx, existing := range results {
		if existing.ID == result.ID || existing.ExecutionRecordID == result.ExecutionRecordID {
			results[idx] = result
			s.verify[incidentID] = results
			return nil
		}
	}

	s.verify[incidentID] = append(results, result)
	return nil
}

func (s *MemoryStore) GetVerificationResult(_ context.Context, executionRecordID string) (domain.VerificationResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, results := range s.verify {
		for _, result := range results {
			if result.ExecutionRecordID == executionRecordID {
				return result, nil
			}
		}
	}

	return domain.VerificationResult{}, ErrNotFound
}

func (s *MemoryStore) ListVerificationResults(_ context.Context, incidentID string) ([]domain.VerificationResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	results := append([]domain.VerificationResult(nil), s.verify[incidentID]...)
	sort.SliceStable(results, func(i, j int) bool {
		return results[i].CreatedAt.Before(results[j].CreatedAt)
	})

	return results, nil
}

func (s *MemoryStore) ListVerificationResultsByAction(_ context.Context, actionID string) ([]domain.VerificationResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var executionIDs map[string]struct{}
	for _, actions := range s.actions {
		for _, action := range actions {
			if action.ID != actionID {
				continue
			}
			executionIDs = make(map[string]struct{})
			break
		}
		if executionIDs != nil {
			break
		}
	}

	if executionIDs == nil {
		return nil, ErrNotFound
	}

	for _, incidentRecords := range s.executions {
		for _, record := range incidentRecords {
			if record.CandidateActionID == actionID {
				executionIDs[record.ID] = struct{}{}
			}
		}
	}

	var results []domain.VerificationResult
	for _, incidentResults := range s.verify {
		for _, result := range incidentResults {
			if _, ok := executionIDs[result.ExecutionRecordID]; ok {
				results = append(results, result)
			}
		}
	}

	sort.SliceStable(results, func(i, j int) bool {
		return results[i].CreatedAt.Before(results[j].CreatedAt)
	})

	return results, nil
}
