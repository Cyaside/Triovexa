package coderepair

import (
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/Cyaside/Triovexa/internal/domain"
)

var gitRevision = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

func ValidGitRevision(revision string) bool { return gitRevision.MatchString(revision) }

// TrustedDeployedRevision uses only a fresh, authenticated workload-control
// observation for the incident target. Alert text is not revision provenance.
func TrustedDeployedRevision(incident domain.Incident, items []domain.EvidenceItem, now time.Time) (string, error) {
	var revision string
	for _, item := range items {
		if item.IncidentID != incident.ID || item.Type != "metric" || item.Source != "workload-control" ||
			item.Timestamp.IsZero() || item.Timestamp.After(now.Add(5*time.Second)) || now.Sub(item.Timestamp) > time.Minute {
			continue
		}
		var metadata struct {
			Target           string `json:"target"`
			Complete         bool   `json:"complete"`
			DeployedRevision string `json:"deployed_revision"`
		}
		if json.Unmarshal([]byte(item.MetadataJSON), &metadata) != nil || !metadata.Complete ||
			metadata.Target != incident.ServiceName || !ValidGitRevision(metadata.DeployedRevision) {
			continue
		}
		if revision != "" && revision != metadata.DeployedRevision {
			return "", errors.New("conflicting deployed revisions in workload evidence")
		}
		revision = metadata.DeployedRevision
	}
	if revision == "" {
		return "", errors.New("fresh trusted deployed revision is unavailable")
	}
	return revision, nil
}
