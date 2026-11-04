package verification

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
)

type HTTPObservation struct {
	Healthy  bool    `json:"healthy"`
	Requests float64 `json:"requests"`
	Errors   float64 `json:"errors"`
}

func (s Sample) validHTTP(now time.Time) bool {
	h := s.HTTP
	return h != nil && s.Complete && s.AlertObserved && s.Source == "prometheus-http-v1" && s.Target != "" && s.Environment != "" &&
		len(s.ProfileSHA256) == 64 && coderepair.ValidGitRevision(s.DeployedRevision) && !s.Timestamp.IsZero() &&
		!s.Timestamp.After(now.Add(5*time.Second)) && now.Sub(s.Timestamp) <= time.Minute &&
		finite(h.Requests) && finite(h.Errors) && h.Requests >= 0 && h.Errors >= 0 && h.Errors <= h.Requests
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func (s Sample) Matches(contract *coderepair.TelemetryContract) bool {
	if contract == nil {
		return s.HTTP == nil && s.ProfileSHA256 == "" // Historical queue records only.
	}
	if contract.Validate() != nil || s.Target != contract.ServiceName || s.Environment != contract.Environment || s.ProfileSHA256 != contract.Digest() {
		return false
	}
	return (contract.Profile.Kind == "http" && s.HTTP != nil && s.Source == "prometheus-http-v1") ||
		(contract.Profile.Kind == "queue" && s.HTTP == nil && s.Source == "redis-streams")
}

func CheckFor(contract *coderepair.TelemetryContract, before, after Sample, targetSHA string) bool {
	if !before.Matches(contract) || !after.Matches(contract) || !coderepair.ValidGitRevision(targetSHA) ||
		!coderepair.ValidGitRevision(before.DeployedRevision) || before.DeployedRevision == targetSHA || after.DeployedRevision != targetSHA || !after.Timestamp.After(before.Timestamp) || !before.AlertObserved || before.AlertCleared {
		return false
	}
	if contract == nil || contract.Profile.Kind == "queue" {
		return Check(before, after, targetSHA)
	}
	p := contract.Profile.HTTP
	b, a := before.HTTP, after.HTTP
	return before.Complete && after.Complete && before.AlertObserved && !before.AlertCleared && after.AlertObserved && after.AlertCleared &&
		finite(b.Requests) && finite(a.Requests) && b.Requests >= p.MinimumRequests && a.Requests >= p.MinimumRequests &&
		finite(b.Errors) && finite(a.Errors) && b.Errors >= 0 && a.Errors >= 0 && b.Errors <= b.Requests && a.Errors <= a.Requests &&
		(!b.Healthy || b.Errors/b.Requests > p.MaxErrorRatio) && a.Healthy && a.Errors/a.Requests <= p.MaxErrorRatio
}

type ClientConfig struct {
	WorkloadURL, WorkloadToken, PrometheusURL string
}

type SnapshotClient interface {
	Snapshot(context.Context) (Sample, error)
}

// NewClient is shared by evidence intake, deployment baseline and verifier.
func NewClient(cfg ClientConfig, contract *coderepair.TelemetryContract) (SnapshotClient, error) {
	if contract == nil {
		return NewWorkloadClient(cfg.WorkloadURL, cfg.WorkloadToken, cfg.PrometheusURL)
	}
	if err := contract.Validate(); err != nil {
		return nil, err
	}
	if contract.Profile.Kind == "http" {
		return newHTTPClient(cfg.PrometheusURL, contract)
	}
	c, err := NewWorkloadClient(cfg.WorkloadURL, cfg.WorkloadToken, cfg.PrometheusURL)
	if err != nil {
		return nil, err
	}
	return &queueProfileClient{workload: c, contract: contract}, nil
}

type queueProfileClient struct {
	workload *WorkloadClient
	contract *coderepair.TelemetryContract
}

func (c *queueProfileClient) Snapshot(ctx context.Context) (Sample, error) {
	s, err := c.workload.snapshot(ctx)
	if err != nil {
		return Sample{}, err
	}
	if s.Target != c.contract.ServiceName || s.Environment != c.contract.Environment {
		return Sample{}, errors.New("queue observation belongs to another service or environment")
	}
	cleared, err := readAlertStatus(ctx, c.workload.client, c.workload.alertsEndpoint, c.workload.rulesEndpoint, c.contract)
	if err != nil {
		return Sample{}, err
	}
	s.ProfileSHA256 = c.contract.Digest()
	s.AlertObserved, s.AlertCleared = true, cleared
	if !s.Valid(time.Now().UTC()) || !s.Matches(c.contract) {
		return Sample{}, errors.New("queue observation does not match telemetry contract")
	}
	return s, nil
}
