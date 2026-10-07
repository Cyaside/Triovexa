package coderepair

import (
	"errors"
	"time"

	"github.com/Cyaside/Triovexa/internal/domain"
)

// This grant authorizes investigation only. Automatic publication is separate.
type AutomationPolicy struct {
	Enabled           bool      `json:"enabled"`
	AuthorizedBy      string    `json:"authorized_by"`
	ExpiresAt         time.Time `json:"expires_at"`
	CampaignID        string    `json:"campaign_id"`
	MaxInvestigations int       `json:"max_investigations"`
	MaxModelRequests  int       `json:"max_model_requests"`
}

func (p AutomationPolicy) Validate() error {
	if !p.Enabled {
		return nil
	}
	if !safeEvidenceIdentifier(p.AuthorizedBy) || !safeEvidenceIdentifier(p.CampaignID) || p.ExpiresAt.IsZero() ||
		p.MaxInvestigations < 1 || p.MaxInvestigations > 100 || p.MaxModelRequests < 1 || p.MaxModelRequests > 20 {
		return errors.New("automatic investigation requires an admin grant, expiry, campaign and bounded investigation/model limits")
	}
	return nil
}

func EligibleForInvestigation(incident domain.Incident, binding RepositoryBinding) bool {
	if incident.State == domain.IncidentStateEscalated || incident.State == domain.IncidentStateFailedRemediation {
		return true
	}
	if binding.Automation == nil || !binding.Automation.Enabled {
		return false
	}
	return incident.State == domain.IncidentStateDetected || incident.State == domain.IncidentStateTriaging
}
