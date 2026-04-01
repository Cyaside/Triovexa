package alerting

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type GrafanaWebhookPayload struct {
	Title        string            `json:"title"`
	Message      string            `json:"message"`
	State        string            `json:"state"`
	CommonLabels map[string]string `json:"commonLabels"`
	Alerts       []GrafanaAlert    `json:"alerts"`
}

type GrafanaAlert struct {
	Status       string            `json:"status"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     time.Time         `json:"startsAt"`
	EndsAt       time.Time         `json:"endsAt"`
	Fingerprint  string            `json:"fingerprint"`
	GeneratorURL string            `json:"generatorURL"`
}

type NormalizedAlert struct {
	ExternalAlertID string
	Title           string
	ServiceName     string
	Environment     string
	Severity        string
	AlertSource     string
	StartedAt       time.Time
	Labels          map[string]string
}

func NormalizeGrafanaPayload(payload GrafanaWebhookPayload) (NormalizedAlert, error) {
	if len(payload.Alerts) == 0 {
		return NormalizedAlert{}, errors.New("grafana webhook payload must contain at least one alert")
	}

	primary := payload.Alerts[0]
	labels := mergeLabels(payload.CommonLabels, primary.Labels)

	title := strings.TrimSpace(payload.Title)
	if title == "" {
		title = firstNonEmpty(
			primary.Annotations["summary"],
			primary.Annotations["description"],
			"grafana alert",
		)
	}

	serviceName := firstNonEmpty(
		labels["service"],
		labels["service_name"],
		labels["job"],
		"unknown-service",
	)

	environment := firstNonEmpty(
		labels["environment"],
		labels["env"],
		"unknown-environment",
	)

	severity := firstNonEmpty(labels["severity"], "warning")
	externalAlertID := firstNonEmpty(
		primary.Fingerprint,
		fmt.Sprintf("%s-%s-%d", serviceName, environment, primary.StartsAt.UTC().Unix()),
	)

	return NormalizedAlert{
		ExternalAlertID: externalAlertID,
		Title:           title,
		ServiceName:     serviceName,
		Environment:     environment,
		Severity:        severity,
		AlertSource:     "grafana",
		StartedAt:       primary.StartsAt.UTC(),
		Labels:          labels,
	}, nil
}

func mergeLabels(sets ...map[string]string) map[string]string {
	merged := make(map[string]string)
	for _, set := range sets {
		for key, value := range set {
			merged[key] = value
		}
	}

	return merged
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}

	return ""
}
