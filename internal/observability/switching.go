package observability

import (
	"context"

	"github.com/Cyaside/Triovexa/internal/demo"
	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/mode"
)

type SwitchingCollector struct {
	modes *mode.Manager
	demo  interface {
		Collect(context.Context, domain.Incident) ([]domain.EvidenceItem, error)
	}
	grafana interface {
		Collect(context.Context, domain.Incident) ([]domain.EvidenceItem, error)
	}
}

func NewSwitchingCollector(
	modes *mode.Manager,
	demoCollector interface {
		Collect(context.Context, domain.Incident) ([]domain.EvidenceItem, error)
	},
	grafanaCollector interface {
		Collect(context.Context, domain.Incident) ([]domain.EvidenceItem, error)
	},
) *SwitchingCollector {
	return &SwitchingCollector{
		modes:   modes,
		demo:    demoCollector,
		grafana: grafanaCollector,
	}
}

func (c *SwitchingCollector) Collect(ctx context.Context, incident domain.Incident) ([]domain.EvidenceItem, error) {
	if c.modes != nil && c.modes.Snapshot().Observability == mode.ObservabilityGrafana && c.grafana != nil {
		return c.grafana.Collect(ctx, incident)
	}

	return c.demo.Collect(ctx, incident)
}

type SwitchingSnapshotFetcher struct {
	modes *mode.Manager
	demo  interface {
		Snapshot(context.Context) (demo.Snapshot, error)
	}
	grafana interface {
		Snapshot(context.Context) (demo.Snapshot, error)
	}
}

func NewSwitchingSnapshotFetcher(
	modes *mode.Manager,
	demoFetcher interface {
		Snapshot(context.Context) (demo.Snapshot, error)
	},
	grafanaFetcher interface {
		Snapshot(context.Context) (demo.Snapshot, error)
	},
) *SwitchingSnapshotFetcher {
	return &SwitchingSnapshotFetcher{
		modes:   modes,
		demo:    demoFetcher,
		grafana: grafanaFetcher,
	}
}

func (f *SwitchingSnapshotFetcher) Snapshot(ctx context.Context) (demo.Snapshot, error) {
	if f.modes != nil && f.modes.Snapshot().Observability == mode.ObservabilityGrafana && f.grafana != nil {
		return f.grafana.Snapshot(ctx)
	}

	return f.demo.Snapshot(ctx)
}
