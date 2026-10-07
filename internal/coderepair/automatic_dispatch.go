package coderepair

import (
	"context"
	"errors"
	"time"

	"github.com/Cyaside/Triovexa/internal/domain"
)

type AutomaticDispatchRepository interface {
	ProposalRepository
	HasAutomaticInvestigation(context.Context, string, string) (bool, error)
	CreateAutomaticInvestigation(context.Context, Case, Event, EvidenceSnapshot, AgentSelection) (bool, error)
}

type AutomaticDispatcher struct {
	Store       AutomaticDispatchRepository
	ResolveBase BaseRevisionResolver
	Selection   func() (AgentSelection, error)
	Allowed     func(context.Context) error
}

// Try consumes the alert workflow when automatic investigation is configured.
// Errors are explicit: no fallback to operational remediation or another model.
func (d *AutomaticDispatcher) Try(ctx context.Context, incident domain.Incident) (bool, error) {
	if d.Store == nil || d.Selection == nil || d.ResolveBase == nil || d.Allowed == nil {
		return false, errors.New("automatic investigation is not configured")
	}
	b, err := d.Store.GetActiveRepositoryBinding(ctx, incident.ServiceName, incident.Environment)
	if err != nil {
		if errors.Is(err, ErrRepositoryBindingNotFound) {
			return false, nil
		}
		return false, err
	}
	if b.Automation == nil || !b.Automation.Enabled {
		return false, nil
	}
	if err = d.Allowed(ctx); err != nil {
		return true, err
	}
	p := b.Automation
	now := time.Now().UTC()
	if p.Validate() != nil || !p.ExpiresAt.After(now) {
		return true, errors.New("automatic investigation grant has expired or is invalid")
	}
	exists, err := d.Store.HasAutomaticInvestigation(ctx, incident.ID, b.ID)
	if err != nil || exists {
		return true, err
	}
	selection, err := d.Selection()
	if err != nil {
		return true, err
	}
	if selection.Runtime == nil || selection.Runtime.CampaignID != p.CampaignID {
		return true, errors.New("automatic investigation must use the granted model campaign")
	}
	if selection.Runtime.Profile == "final-smoke" {
		return true, errors.New("final-smoke requires explicit validation and cannot run from automatic alerts")
	}
	runtime := *selection.Runtime
	runtime.MaxModelRequests = p.MaxModelRequests
	selection.Runtime = &runtime
	service, err := NewProposalService(d.Store, d.ResolveBase, EvidenceLimits{MaxItems: 20, MaxSnippetBytes: 1024, MaxTotalBytes: 16 * 1024, MaxAge: time.Minute})
	if err != nil {
		return true, err
	}
	c, event, snapshot, err := service.prepare(ctx, incident, b, p.AuthorizedBy, now)
	if err != nil {
		return true, err
	}
	_, err = d.Store.CreateAutomaticInvestigation(ctx, c, event, snapshot, selection)
	return true, err
}
