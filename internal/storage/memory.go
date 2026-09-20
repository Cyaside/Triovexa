package storage

import (
	"context"
	"errors"
	"sort"
	"strings"
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
	rollbacks  map[string][]domain.RollbackRecord
	triageByID map[string]domain.TriageResult
	users      map[string]domain.User
	sessions   map[string]domain.Session
	settings   map[string]string
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
		rollbacks:  make(map[string][]domain.RollbackRecord),
		triageByID: make(map[string]domain.TriageResult),
		users:      make(map[string]domain.User),
		sessions:   make(map[string]domain.Session),
		settings:   make(map[string]string),
	}
}

func (s *MemoryStore) GetSetting(_ context.Context, key string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.settings[key]
	if !ok {
		return "", ErrNotFound
	}
	return value, nil
}

func (s *MemoryStore) PutSetting(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings[key] = value
	return nil
}

func (s *MemoryStore) CreateUser(_ context.Context, user domain.User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.users {
		if strings.EqualFold(existing.Username, user.Username) {
			return errors.New("username already exists")
		}
	}
	s.users[user.ID] = user
	return nil
}

func (s *MemoryStore) GetUserByUsername(_ context.Context, username string) (domain.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, user := range s.users {
		if strings.EqualFold(user.Username, username) {
			return user, nil
		}
	}
	return domain.User{}, ErrNotFound
}

func (s *MemoryStore) GetUser(_ context.Context, id string) (domain.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	user, ok := s.users[id]
	if !ok {
		return domain.User{}, ErrNotFound
	}
	return user, nil
}

func (s *MemoryStore) CreateSession(_ context.Context, session domain.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[session.TokenHash] = session
	return nil
}

func (s *MemoryStore) GetSession(_ context.Context, tokenHash string) (domain.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, ok := s.sessions[tokenHash]
	if !ok || !session.ExpiresAt.After(time.Now()) {
		return domain.Session{}, ErrNotFound
	}
	return session, nil
}

func (s *MemoryStore) DeleteSession(_ context.Context, tokenHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, tokenHash)
	return nil
}

func (s *MemoryStore) Close() error { return nil }

func (s *MemoryStore) Ping(context.Context) error { return nil }

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

func (s *MemoryStore) CompareAndSwapIncidentState(_ context.Context, incidentID string, expected, next domain.IncidentState) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	incident, ok := s.incidents[incidentID]
	if !ok {
		return false, ErrNotFound
	}
	if incident.State != expected {
		return false, nil
	}
	incident.State = next
	incident.UpdatedAt = time.Now().UTC()
	s.incidents[incidentID] = incident
	return true, nil
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

func (s *MemoryStore) GetLatestIncidentByExternalAlertID(_ context.Context, externalAlertID string) (domain.Incident, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var latest domain.Incident
	var found bool
	for _, incident := range s.incidents {
		if incident.ExternalAlertID != externalAlertID {
			continue
		}
		if !found || incident.CreatedAt.After(latest.CreatedAt) {
			latest = incident
			found = true
		}
	}
	if !found {
		return domain.Incident{}, ErrNotFound
	}

	return latest, nil
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

func (s *MemoryStore) DecideApproval(_ context.Context, incidentID string, record domain.ApprovalRecord, expectedAction, nextAction domain.CandidateActionStatus, expectedIncident, nextIncident domain.IncidentState) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	actions := s.actions[incidentID]
	index := -1
	for i := range actions {
		if actions[i].ID == record.CandidateActionID {
			index = i
			break
		}
	}
	if index < 0 {
		return false, ErrNotFound
	}
	if actions[index].Status != expectedAction {
		return false, nil
	}
	incident, ok := s.incidents[incidentID]
	if !ok {
		return false, ErrNotFound
	}
	if expectedIncident != "" && incident.State != expectedIncident {
		return false, nil
	}
	actions[index].Status = nextAction
	s.actions[incidentID] = actions
	s.approvals[record.CandidateActionID] = append(s.approvals[record.CandidateActionID], record)
	if nextIncident != "" {
		incident.State = nextIncident
		incident.UpdatedAt = time.Now().UTC()
		s.incidents[incidentID] = incident
	}
	return true, nil
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

func (s *MemoryStore) ClaimExecution(_ context.Context, incidentID string, actionID string, record domain.ExecutionRecord) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	incident, ok := s.incidents[incidentID]
	if !ok {
		return false, ErrNotFound
	}
	actions := s.actions[incidentID]
	actionIndex := -1
	for index, action := range actions {
		if action.ID == actionID {
			actionIndex = index
			break
		}
	}
	if actionIndex < 0 {
		return false, ErrNotFound
	}
	status := actions[actionIndex].Status
	if status != domain.CandidateActionStatusApproved && status != domain.CandidateActionStatusAllowed {
		return false, nil
	}
	for _, existing := range s.executions[incidentID] {
		if existing.CandidateActionID == actionID {
			return false, nil
		}
	}
	target := actions[actionIndex].TargetResource
	for candidateIncidentID, records := range s.executions {
		for _, existing := range records {
			if existing.Status != "started" {
				continue
			}
			for _, candidate := range s.actions[candidateIncidentID] {
				if candidate.ID == existing.CandidateActionID && candidate.TargetResource == target {
					return false, nil
				}
			}
		}
	}
	actions[actionIndex].Status = domain.CandidateActionStatusExecuting
	s.actions[incidentID] = actions
	incident.State = domain.IncidentStateExecutingAction
	incident.UpdatedAt = time.Now().UTC()
	s.incidents[incidentID] = incident
	s.executions[incidentID] = append(s.executions[incidentID], record)
	return true, nil
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

func (s *MemoryStore) ListExecutionRecordsByStatus(_ context.Context, status string) ([]domain.ExecutionRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var records []domain.ExecutionRecord
	for _, incidentRecords := range s.executions {
		for _, record := range incidentRecords {
			if record.Status == status {
				records = append(records, record)
			}
		}
	}
	sort.SliceStable(records, func(i, j int) bool { return records[i].StartedAt.Before(records[j].StartedAt) })
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

func (s *MemoryStore) SaveRollbackRecord(_ context.Context, record domain.RollbackRecord) error {
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

	records := s.rollbacks[incidentID]
	for idx, existing := range records {
		if existing.ID == record.ID {
			records[idx] = record
			s.rollbacks[incidentID] = records
			return nil
		}
	}

	s.rollbacks[incidentID] = append(records, record)
	return nil
}

func (s *MemoryStore) ListRollbackRecords(_ context.Context, incidentID string) ([]domain.RollbackRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	records := append([]domain.RollbackRecord(nil), s.rollbacks[incidentID]...)
	sort.SliceStable(records, func(i, j int) bool {
		return records[i].StartedAt.Before(records[j].StartedAt)
	})

	return records, nil
}

func (s *MemoryStore) ListRollbackRecordsByAction(_ context.Context, actionID string) ([]domain.RollbackRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var records []domain.RollbackRecord
	for _, incidentRecords := range s.rollbacks {
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
