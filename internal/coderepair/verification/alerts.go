package verification

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
)

func readAlertStatus(ctx context.Context, client *http.Client, alertsURL, rulesURL string, contract *coderepair.TelemetryContract) (bool, error) {
	alertName := "TriovexaQueueBacklogHigh"
	if contract != nil {
		alertName = contract.Profile.AlertName
	}
	read := func(endpoint string, target any) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}
		response, err := client.Do(req)
		if err != nil {
			return errors.New("Prometheus alert observation unavailable")
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 256*1024)).Decode(target) != nil {
			return errors.New("Prometheus alert observation is invalid")
		}
		return nil
	}
	var rules struct {
		Status string `json:"status"`
		Data   struct {
			Groups []struct {
				Rules []struct {
					Name, Type, Health string
					LastEvaluation     time.Time `json:"lastEvaluation"`
				} `json:"rules"`
			} `json:"groups"`
		} `json:"data"`
	}
	if err := read(rulesURL, &rules); err != nil {
		return false, err
	}
	if rules.Status != "success" {
		return false, errors.New("Prometheus rules unavailable")
	}
	found := 0
	now := time.Now().UTC()
	for _, group := range rules.Data.Groups {
		for _, rule := range group.Rules {
			if rule.Name == alertName {
				if rule.Type != "alerting" || rule.Health != "ok" || (contract != nil && (rule.LastEvaluation.IsZero() || now.Sub(rule.LastEvaluation) > time.Minute || rule.LastEvaluation.After(now.Add(5*time.Second)))) {
					return false, errors.New("configured alert rule is stale or unhealthy")
				}
				found++
			}
		}
	}
	if found != 1 {
		return false, errors.New("configured alert rule is missing or ambiguous")
	}
	var alerts struct {
		Status string `json:"status"`
		Data   struct {
			Alerts []struct {
				Labels map[string]string `json:"labels"`
				State  string            `json:"state"`
			} `json:"alerts"`
		} `json:"data"`
	}
	if err := read(alertsURL, &alerts); err != nil {
		return false, err
	}
	if alerts.Status != "success" || alerts.Data.Alerts == nil {
		return false, errors.New("Prometheus alerts unavailable")
	}
	serviceLabel, environmentLabel := "service", "environment"
	if contract != nil && contract.Profile.HTTP != nil {
		serviceLabel, environmentLabel = contract.Profile.HTTP.ServiceLabel, contract.Profile.HTTP.EnvironmentLabel
	}
	for _, alert := range alerts.Data.Alerts {
		if contract != nil && alert.Labels["alertname"] == alertName && (alert.Labels[serviceLabel] == "" || alert.Labels[environmentLabel] == "") {
			return false, errors.New("configured alert has incomplete target identity")
		}
		if alert.Labels["alertname"] == alertName && (contract == nil || (alert.Labels[serviceLabel] == contract.ServiceName && alert.Labels[environmentLabel] == contract.Environment)) && alert.State != "inactive" {
			return false, nil
		}
	}
	return true, nil
}
