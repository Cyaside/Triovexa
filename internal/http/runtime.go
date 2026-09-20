package http

import (
	"context"

	"github.com/Cyaside/Triovexa/internal/auth"
	"github.com/Cyaside/Triovexa/internal/mode"
	"github.com/Cyaside/Triovexa/internal/observability"
	"github.com/Cyaside/Triovexa/internal/readiness"
)

type RuntimeControls struct {
	Modes     *mode.Manager
	Providers ProviderStatus
	Auth      *auth.Service
	Readiness interface {
		Check(context.Context) readiness.Report
	}
}

type ProviderStatus struct {
	MistralConfigured  bool
	GrafanaConfigured  bool
	MistralModel       string
	LLMConfigured      bool
	LLMProvider        string
	LLMModel           string
	MetricsSourceUID   string
	LogsSourceUID      string
	GrafanaDatasources interface {
		ListDatasources(context.Context) ([]observability.DatasourceSummary, error)
	}
}

type runtimeViewData struct {
	ReasoningMode     string
	ObservabilityMode string
	MistralConfigured bool
	GrafanaConfigured bool
	MistralModel      string
	LLMConfigured     bool
	LLMProvider       string
	LLMModel          string
	MetricsSourceUID  string
	LogsSourceUID     string
	DatasourceCount   int
	DatasourceError   string
	AvailableSources  []observability.DatasourceSummary
}

func buildRuntimeViewData(ctx context.Context, controls *RuntimeControls) runtimeViewData {
	view := runtimeViewData{}
	if controls == nil {
		return view
	}

	if controls.Modes != nil {
		snapshot := controls.Modes.Snapshot()
		view.ReasoningMode = string(snapshot.Reasoning)
		view.ObservabilityMode = string(snapshot.Observability)
	}
	view.MistralConfigured = controls.Providers.MistralConfigured
	view.GrafanaConfigured = controls.Providers.GrafanaConfigured
	view.MistralModel = controls.Providers.MistralModel
	view.LLMConfigured = controls.Providers.LLMConfigured
	view.LLMProvider = controls.Providers.LLMProvider
	view.LLMModel = controls.Providers.LLMModel
	view.MetricsSourceUID = controls.Providers.MetricsSourceUID
	view.LogsSourceUID = controls.Providers.LogsSourceUID
	if controls.Providers.GrafanaDatasources != nil {
		items, err := controls.Providers.GrafanaDatasources.ListDatasources(ctx)
		if err != nil {
			view.DatasourceError = err.Error()
		} else {
			view.DatasourceCount = len(items)
			if len(items) > 6 {
				items = items[:6]
			}
			view.AvailableSources = items
		}
	}

	return view
}
