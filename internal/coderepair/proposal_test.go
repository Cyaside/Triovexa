package coderepair

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/domain"
)

type proposalRepository struct {
	incident  domain.Incident
	binding   RepositoryBinding
	items     []domain.EvidenceItem
	created   []Case
	events    []Event
	snapshots []EvidenceSnapshot
}

func (r *proposalRepository) GetIncident(_ context.Context, id string) (domain.Incident, error) {
	if id != r.incident.ID {
		return domain.Incident{}, errors.New("incident not found")
	}
	return r.incident, nil
}

func (r *proposalRepository) GetActiveRepositoryBinding(_ context.Context, service, environment string) (RepositoryBinding, error) {
	if service != r.binding.ServiceName || environment != r.binding.Environment {
		return RepositoryBinding{}, errors.New("binding not found")
	}
	return r.binding, nil
}

func (r *proposalRepository) ListEvidenceItems(_ context.Context, incidentID string) ([]domain.EvidenceItem, error) {
	if incidentID != r.incident.ID {
		return nil, errors.New("evidence target mismatch")
	}
	return r.items, nil
}

func (r *proposalRepository) CreateRepairProposal(_ context.Context, c Case, event Event, snapshot EvidenceSnapshot) error {
	r.created = append(r.created, c)
	r.events = append(r.events, event)
	r.snapshots = append(r.snapshots, snapshot)
	return nil
}

func TestProposalCreatesApprovalReadyCaseFromTrustedEvidence(t *testing.T) {
	now := time.Now().UTC()
	incident, binding, items := evidenceFixture(now)
	repo := &proposalRepository{incident: incident, binding: binding, items: items}
	baseSHA := strings.Repeat("b", 40)
	resolved := 0
	service, err := NewProposalService(repo, func(_ context.Context, got RepositoryBinding) (string, error) {
		resolved++
		if got.ID != binding.ID {
			t.Fatal("resolved a different repository binding")
		}
		return baseSHA, nil
	}, EvidenceLimits{MaxItems: 10, MaxSnippetBytes: 2048, MaxTotalBytes: 4096, MaxAge: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	c, evidence, err := service.Propose(context.Background(), incident.ID, "operator-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != 1 || len(repo.created) != 1 || len(repo.events) != 1 || len(repo.snapshots) != 1 {
		t.Fatalf("resolver calls=%d, cases=%d, events=%d", resolved, len(repo.created), len(repo.events))
	}
	if c.State != StateAwaitingInvestigationApproval || c.Version != 1 || c.BaseSHA != baseSHA ||
		c.DeployedSHA != strings.Repeat("a", 40) || !evidence.VerifyDigest() {
		t.Fatalf("invalid proposed case or evidence: %+v %+v", c, evidence)
	}
	if digest, err := ScopeDigest(c, binding); err != nil || c.ScopeDigest != digest {
		t.Fatalf("scope digest mismatch: %s, %v", digest, err)
	}
	if repo.events[0].CaseID != c.ID || repo.events[0].Type != "investigation_requested" ||
		!strings.Contains(repo.events[0].DetailsJSON, evidence.SHA256) {
		t.Fatalf("missing evidence audit: %+v", repo.events[0])
	}
	for _, secret := range []string{"abc123", "alice@example.com", "raw-secret-metadata"} {
		if strings.Contains(repo.events[0].DetailsJSON, secret) {
			t.Fatalf("audit leaked %q", secret)
		}
	}
}

func TestProposalRejectsInsufficientEvidenceBeforeRemoteLookup(t *testing.T) {
	now := time.Now().UTC()
	incident, binding, items := evidenceFixture(now)
	limits := EvidenceLimits{MaxItems: 10, MaxSnippetBytes: 2048, MaxTotalBytes: 4096, MaxAge: time.Minute}
	for name, mutate := range map[string]func(*proposalRepository){
		"missing log":       func(r *proposalRepository) { r.items = r.items[:1] },
		"missing revision":  func(r *proposalRepository) { r.items[0].MetadataJSON = `{}` },
		"disabled binding":  func(r *proposalRepository) { r.binding.Enabled = false },
		"resolved incident": func(r *proposalRepository) { r.incident.State = domain.IncidentStateResolved },
	} {
		t.Run(name, func(t *testing.T) {
			repo := &proposalRepository{incident: incident, binding: binding, items: append([]domain.EvidenceItem(nil), items...)}
			mutate(repo)
			resolved := false
			service, err := NewProposalService(repo, func(context.Context, RepositoryBinding) (string, error) {
				resolved = true
				return strings.Repeat("b", 40), nil
			}, limits)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := service.Propose(context.Background(), incident.ID, "operator-1", now); err == nil {
				t.Fatal("proposal accepted insufficient scope or evidence")
			}
			if resolved || len(repo.created) != 0 {
				t.Fatal("remote lookup or case creation ran after a failed gate")
			}
		})
	}
}

func TestProposalRejectsInvalidBaseRevision(t *testing.T) {
	now := time.Now().UTC()
	incident, binding, items := evidenceFixture(now)
	repo := &proposalRepository{incident: incident, binding: binding, items: items}
	service, err := NewProposalService(repo, func(context.Context, RepositoryBinding) (string, error) {
		return "main", nil
	}, EvidenceLimits{MaxItems: 10, MaxSnippetBytes: 2048, MaxTotalBytes: 4096, MaxAge: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Propose(context.Background(), incident.ID, "operator-1", now); err == nil || len(repo.created) != 0 {
		t.Fatal("proposal accepted unpinned repository base")
	}
}
