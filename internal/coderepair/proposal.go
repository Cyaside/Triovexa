package coderepair

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/security"
)

type ProposalRepository interface {
	GetIncident(context.Context, string) (domain.Incident, error)
	GetActiveRepositoryBinding(context.Context, string, string) (RepositoryBinding, error)
	ListEvidenceItems(context.Context, string) ([]domain.EvidenceItem, error)
	CreateRepairCase(context.Context, Case, Event) error
}

type BaseRevisionResolver func(context.Context, RepositoryBinding) (string, error)

type ProposalService struct {
	repository  ProposalRepository
	resolveBase BaseRevisionResolver
	limits      EvidenceLimits
}

func NewProposalService(repository ProposalRepository, resolveBase BaseRevisionResolver, limits EvidenceLimits) (*ProposalService, error) {
	if repository == nil || resolveBase == nil {
		return nil, errors.New("repair proposal requires repository and base revision resolver")
	}
	if err := limits.validate(); err != nil {
		return nil, err
	}
	return &ProposalService{repository: repository, resolveBase: resolveBase, limits: limits}, nil
}

// Propose evaluates incident evidence before creating an approval-ready case.
// The store performs its own target and revision checks inside the insert tx.
func (s *ProposalService) Propose(ctx context.Context, incidentID, actorID string, now time.Time) (Case, EvidenceSnapshot, error) {
	if incidentID == "" || actorID == "" || now.IsZero() {
		return Case{}, EvidenceSnapshot{}, errors.New("repair proposal requires incident, actor and time")
	}
	incident, err := s.repository.GetIncident(ctx, incidentID)
	if err != nil {
		return Case{}, EvidenceSnapshot{}, fmt.Errorf("load incident: %w", err)
	}
	binding, err := s.repository.GetActiveRepositoryBinding(ctx, incident.ServiceName, incident.Environment)
	if err != nil {
		return Case{}, EvidenceSnapshot{}, fmt.Errorf("load repository binding: %w", err)
	}
	snapshot, err := CaptureEvidence(ctx, s.repository, incident, now, s.limits)
	if err != nil {
		return Case{}, EvidenceSnapshot{}, err
	}
	if err := CheckInvestigationEvidence(incident, binding, snapshot, now); err != nil {
		return Case{}, snapshot, err
	}
	baseSHA, err := s.resolveBase(ctx, binding)
	if err != nil {
		return Case{}, snapshot, fmt.Errorf("resolve registered base revision: %s", security.Redact(err.Error()))
	}
	if !ValidGitRevision(baseSHA) {
		return Case{}, snapshot, errors.New("registered base branch returned an invalid revision")
	}
	repairCase := Case{
		ID: uuid.NewString(), IncidentID: incident.ID, BindingID: binding.ID,
		BaseSHA: baseSHA, DeployedSHA: snapshot.DeployedRevision,
		PolicyVersion: binding.PolicyVersion, State: StateAwaitingInvestigationApproval,
		Version: 1, CreatedBy: actorID, CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
	}
	repairCase.ScopeDigest, err = ScopeDigest(repairCase, binding)
	if err != nil {
		return Case{}, snapshot, err
	}
	details, err := json.Marshal(map[string]string{
		"evidence_sha256": snapshot.SHA256,
		"base_sha":        baseSHA,
		"deployed_sha":    snapshot.DeployedRevision,
	})
	if err != nil {
		return Case{}, snapshot, err
	}
	event := Event{ID: uuid.NewString(), CaseID: repairCase.ID, ActorID: actorID,
		Type: "investigation_requested", DetailsJSON: string(details), CreatedAt: now.UTC()}
	if err := s.repository.CreateRepairCase(ctx, repairCase, event); err != nil {
		return Case{}, snapshot, err
	}
	return repairCase, snapshot, nil
}
