package coderepair

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/security"
)

type EvidenceStatus string

const (
	EvidenceAvailable EvidenceStatus = "available"
	EvidenceStale     EvidenceStatus = "stale"
	EvidenceMissing   EvidenceStatus = "missing"
)

type EvidenceEntry struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	Source     string         `json:"source"`
	ObservedAt time.Time      `json:"observed_at"`
	Status     EvidenceStatus `json:"status"`
	Text       string         `json:"text"`
	Untrusted  bool           `json:"untrusted"`
}

type EvidenceSnapshot struct {
	IncidentID       string          `json:"incident_id"`
	ServiceName      string          `json:"service_name"`
	Environment      string          `json:"environment"`
	CapturedAt       time.Time       `json:"captured_at"`
	DeployedRevision string          `json:"deployed_revision,omitempty"`
	Entries          []EvidenceEntry `json:"entries"`
	SHA256           string          `json:"sha256"`
}

type EvidenceLimits struct {
	MaxItems        int
	MaxSnippetBytes int
	MaxTotalBytes   int
	MaxAge          time.Duration
}

type EvidenceLister interface {
	ListEvidenceItems(context.Context, string) ([]domain.EvidenceItem, error)
}

var emailAddress = regexp.MustCompile(`(?i)\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b`)

func (limits EvidenceLimits) validate() error {
	if limits.MaxItems <= 0 || limits.MaxItems > 100 || limits.MaxSnippetBytes <= 0 ||
		limits.MaxSnippetBytes > 4096 || limits.MaxTotalBytes < limits.MaxSnippetBytes ||
		limits.MaxTotalBytes > 128*1024 || limits.MaxAge <= 0 || limits.MaxAge > 24*time.Hour {
		return errors.New("evidence limits are missing or exceed hard caps")
	}
	return nil
}

func CaptureEvidence(ctx context.Context, source EvidenceLister, incident domain.Incident, now time.Time, limits EvidenceLimits) (EvidenceSnapshot, error) {
	if source == nil {
		return EvidenceSnapshot{}, errors.New("evidence source is required")
	}
	items, err := source.ListEvidenceItems(ctx, incident.ID)
	if err != nil {
		return EvidenceSnapshot{}, fmt.Errorf("list incident evidence: %s", security.Redact(err.Error()))
	}
	return BuildEvidenceSnapshot(incident, items, now, limits)
}

// BuildEvidenceSnapshot serializes only known fields. Arbitrary metadata JSON
// and raw logs never cross from incident storage into the repair prompt.
func BuildEvidenceSnapshot(incident domain.Incident, items []domain.EvidenceItem, now time.Time, limits EvidenceLimits) (EvidenceSnapshot, error) {
	if !safeEvidenceIdentifier(incident.ID) || !safeEvidenceIdentifier(incident.ServiceName) ||
		!safeEvidenceIdentifier(incident.Environment) || now.IsZero() {
		return EvidenceSnapshot{}, errors.New("incident evidence scope is incomplete")
	}
	if err := limits.validate(); err != nil {
		return EvidenceSnapshot{}, err
	}
	if len(items) > limits.MaxItems {
		return EvidenceSnapshot{}, errors.New("incident evidence exceeds item budget")
	}
	snapshot := EvidenceSnapshot{
		IncidentID: incident.ID, ServiceName: incident.ServiceName,
		Environment: incident.Environment, CapturedAt: now.UTC(), Entries: make([]EvidenceEntry, 0, len(items)),
	}
	total := 0
	for _, item := range items {
		if !safeEvidenceIdentifier(item.ID) || item.IncidentID != incident.ID ||
			!safeEvidenceIdentifier(item.Source) || !knownEvidenceType(item.Type) {
			return EvidenceSnapshot{}, errors.New("incident evidence contains an unknown source or target")
		}
		if len(item.Snippet) > limits.MaxSnippetBytes {
			return EvidenceSnapshot{}, errors.New("incident evidence snippet exceeds byte budget")
		}
		text := sanitizeEvidence(item.Snippet)
		total += len(text)
		if total > limits.MaxTotalBytes {
			return EvidenceSnapshot{}, errors.New("incident evidence exceeds total byte budget")
		}
		status := EvidenceAvailable
		if item.Timestamp.IsZero() || item.Snippet == "" {
			status = EvidenceMissing
		} else if item.Timestamp.After(now.Add(5*time.Second)) || now.Sub(item.Timestamp) > limits.MaxAge {
			status = EvidenceStale
		}
		snapshot.Entries = append(snapshot.Entries, EvidenceEntry{
			ID: item.ID, Type: item.Type, Source: item.Source,
			ObservedAt: item.Timestamp.UTC(), Status: status, Text: text, Untrusted: true,
		})
	}
	sort.Slice(snapshot.Entries, func(i, j int) bool {
		a, b := snapshot.Entries[i], snapshot.Entries[j]
		if !a.ObservedAt.Equal(b.ObservedAt) {
			return a.ObservedAt.Before(b.ObservedAt)
		}
		return a.ID < b.ID
	})
	if revision, err := TrustedDeployedRevision(incident, items, now); err == nil {
		snapshot.DeployedRevision = revision
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return EvidenceSnapshot{}, err
	}
	digest := sha256.Sum256(data)
	snapshot.SHA256 = hex.EncodeToString(digest[:])
	return snapshot, nil
}

func knownEvidenceType(kind string) bool {
	switch kind {
	case "metric", "log", "trace", "deploy", "service_metadata":
		return true
	default:
		return false
	}
}

func sanitizeEvidence(value string) string {
	value = security.Redact(value)
	value = emailAddress.ReplaceAllString(value, "[REDACTED_EMAIL]")
	return strings.ToValidUTF8(value, "�")
}

func safeEvidenceIdentifier(value string) bool {
	return value != "" && len(value) <= 128 && strings.IndexFunc(value, unicode.IsControl) < 0 &&
		security.Redact(value) == value && emailAddress.ReplaceAllString(value, "") == value
}

func (snapshot EvidenceSnapshot) VerifyDigest() bool {
	if len(snapshot.SHA256) != 64 {
		return false
	}
	expected := snapshot.SHA256
	snapshot.SHA256 = ""
	data, err := json.Marshal(snapshot)
	if err != nil {
		return false
	}
	digest := sha256.Sum256(data)
	return expected == hex.EncodeToString(digest[:])
}

// CheckInvestigationEvidence fails closed when telemetry or repository
// provenance is not adequate for a source-level investigation.
func CheckInvestigationEvidence(incident domain.Incident, binding RepositoryBinding, snapshot EvidenceSnapshot, now time.Time) error {
	if !EligibleForInvestigation(incident, binding) {
		return errors.New("incident is not eligible for code investigation")
	}
	if err := binding.Validate(); err != nil || !binding.Enabled || binding.ServiceName != incident.ServiceName ||
		binding.Environment != incident.Environment {
		return errors.New("no active repository binding matches the incident")
	}
	if !snapshot.VerifyDigest() || snapshot.CapturedAt.IsZero() || snapshot.CapturedAt.After(now.Add(5*time.Second)) ||
		now.Sub(snapshot.CapturedAt) > time.Minute || snapshot.IncidentID != incident.ID || snapshot.ServiceName != incident.ServiceName ||
		snapshot.Environment != incident.Environment || !ValidGitRevision(snapshot.DeployedRevision) {
		return errors.New("fresh deployed revision is unavailable")
	}
	metric, log := false, false
	for _, entry := range snapshot.Entries {
		if entry.Status != EvidenceAvailable {
			continue
		}
		if entry.Type == "metric" && entry.Source == "workload-control" {
			metric = true
		}
		if entry.Type == "log" {
			log = true
		}
	}
	if !metric || !log {
		return errors.New("fresh workload metric and log evidence are required")
	}
	return nil
}
