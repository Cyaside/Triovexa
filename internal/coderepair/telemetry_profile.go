package coderepair

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strings"
)

const TelemetryVersion = "telemetry-v1"

// TelemetryProfile is administrator-owned source and recovery policy. It
// contains metric identifiers and literal labels, never model-supplied PromQL.
type TelemetryProfile struct {
	Version    string       `json:"version"`
	Kind       string       `json:"kind"`
	AlertName  string       `json:"alert_name"`
	LogSources []string     `json:"log_sources"`
	HTTP       *HTTPSignals `json:"http,omitempty"`
}

type HTTPSignals struct {
	RequestsMetric   string            `json:"requests_metric"`
	HealthMetric     string            `json:"health_metric"`
	RevisionMetric   string            `json:"revision_metric"`
	RevisionLabel    string            `json:"revision_label"`
	StatusLabel      string            `json:"status_label"`
	ServiceLabel     string            `json:"service_label"`
	EnvironmentLabel string            `json:"environment_label"`
	RequestLabels    map[string]string `json:"request_labels"`
	WindowSeconds    int               `json:"window_seconds"`
	MinimumRequests  float64           `json:"minimum_requests"`
	MaxErrorRatio    float64           `json:"max_error_ratio"`
}

var metricIdentifier = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]{0,127}$`)
var labelIdentifier = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,63}$`)

func (p TelemetryProfile) Validate() error {
	if p.Version != TelemetryVersion || !safeEvidenceIdentifier(p.AlertName) || len(p.LogSources) == 0 || len(p.LogSources) > 8 {
		return errors.New("telemetry profile requires a supported version, alert name and trusted log sources")
	}
	seen := map[string]bool{}
	for _, source := range p.LogSources {
		if !safeEvidenceIdentifier(source) || seen[source] {
			return errors.New("telemetry log sources are invalid or duplicated")
		}
		seen[source] = true
	}
	if p.Kind == "queue" && p.HTTP == nil {
		return nil
	}
	if p.Kind != "http" || p.HTTP == nil {
		return errors.New("telemetry profile must select queue or configured HTTP signals")
	}
	h := p.HTTP
	for _, name := range []string{h.RequestsMetric, h.HealthMetric, h.RevisionMetric} {
		if !metricIdentifier.MatchString(name) {
			return errors.New("HTTP signals require metric names, not query expressions")
		}
	}
	seen = map[string]bool{}
	for _, name := range []string{h.RevisionLabel, h.StatusLabel, h.ServiceLabel, h.EnvironmentLabel} {
		if !labelIdentifier.MatchString(name) || strings.HasPrefix(name, "__") || seen[name] {
			return errors.New("HTTP signal labels must be distinct valid label names")
		}
		seen[name] = true
	}
	if len(h.RequestLabels) > 8 || h.WindowSeconds < 10 || h.WindowSeconds > 60 ||
		math.IsNaN(h.MinimumRequests) || math.IsInf(h.MinimumRequests, 0) || h.MinimumRequests < 1 || h.MinimumRequests > 1e9 ||
		math.IsNaN(h.MaxErrorRatio) || math.IsInf(h.MaxErrorRatio, 0) || h.MaxErrorRatio < 0 || h.MaxErrorRatio >= 1 {
		return errors.New("HTTP recovery thresholds or window are invalid")
	}
	for label, value := range h.RequestLabels {
		if !labelIdentifier.MatchString(label) || strings.HasPrefix(label, "__") || seen[label] || !safeEvidenceIdentifier(value) {
			return errors.New("request labels cannot replace service, environment, revision or status identity")
		}
	}
	return nil
}

func (b RepositoryBinding) MetricEvidenceSource() string {
	if b.TelemetryProfile != nil && b.TelemetryProfile.Kind == "http" {
		return "repair-prometheus"
	}
	// Bindings saved before telemetry-v1 retain their existing queue contract.
	return "workload-control"
}

func (b RepositoryBinding) AllowsLogSource(source string) bool {
	if b.TelemetryProfile == nil {
		return source != "" // Historical queue binding, not an HTTP default.
	}
	for _, allowed := range b.TelemetryProfile.LogSources {
		if source == allowed {
			return true
		}
	}
	return false
}

// TelemetryContract pins target and policy at deployment start. Historical
// queue deployments have no contract; every newly configured profile is pinned.
type TelemetryContract struct {
	ServiceName string           `json:"service_name"`
	Environment string           `json:"environment"`
	Profile     TelemetryProfile `json:"profile"`
}

func (b RepositoryBinding) TelemetryContract() *TelemetryContract {
	if b.TelemetryProfile == nil {
		return nil
	}
	return &TelemetryContract{ServiceName: b.ServiceName, Environment: b.Environment, Profile: *b.TelemetryProfile}
}

func (c TelemetryContract) Validate() error {
	if !safeEvidenceIdentifier(c.ServiceName) || !safeEvidenceIdentifier(c.Environment) {
		return errors.New("telemetry contract requires an exact service and environment")
	}
	return c.Profile.Validate()
}

func (c *TelemetryContract) Digest() string {
	if c == nil {
		return ""
	}
	if c.Validate() != nil {
		return ""
	}
	data, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
