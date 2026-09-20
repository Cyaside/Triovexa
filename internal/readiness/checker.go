package readiness

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

type Pinger interface{ Ping(context.Context) error }

type Dependency struct {
	Status    string `json:"status"`
	LatencyMS int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
}

type Report struct {
	Status       string                `json:"status"`
	Dependencies map[string]Dependency `json:"dependencies"`
}

type Checker struct {
	storage       Pinger
	redis         *redis.Client
	supervisorURL string
	client        *http.Client
}

func NewChecker(storage Pinger, redisAddress, supervisorURL string) *Checker {
	checker := &Checker{storage: storage, supervisorURL: strings.TrimRight(strings.TrimSpace(supervisorURL), "/"), client: &http.Client{Timeout: 3 * time.Second}}
	if strings.TrimSpace(redisAddress) != "" {
		checker.redis = redis.NewClient(&redis.Options{Addr: strings.TrimSpace(redisAddress)})
	}
	return checker
}

func (c *Checker) Close() error {
	if c == nil || c.redis == nil {
		return nil
	}
	return c.redis.Close()
}

func (c *Checker) Check(ctx context.Context) Report {
	report := Report{Status: "ok", Dependencies: map[string]Dependency{}}
	report.Dependencies["postgresql"] = timedCheck(ctx, c.storage)
	if c.redis == nil {
		report.Dependencies["redis"] = Dependency{Status: "skipped"}
	} else {
		report.Dependencies["redis"] = timedCheck(ctx, redisPinger{client: c.redis})
	}
	if c.supervisorURL == "" {
		report.Dependencies["supervisor"] = Dependency{Status: "skipped"}
	} else {
		report.Dependencies["supervisor"] = c.checkSupervisor(ctx)
	}
	for _, dependency := range report.Dependencies {
		if dependency.Status == "failed" {
			report.Status = "degraded"
			break
		}
	}
	return report
}

type redisPinger struct{ client *redis.Client }

func (p redisPinger) Ping(ctx context.Context) error { return p.client.Ping(ctx).Err() }

func timedCheck(ctx context.Context, pinger Pinger) Dependency {
	if pinger == nil {
		return Dependency{Status: "skipped"}
	}
	started := time.Now()
	err := pinger.Ping(ctx)
	result := Dependency{Status: "ok", LatencyMS: time.Since(started).Milliseconds()}
	if err != nil {
		result.Status = "failed"
		result.Error = "dependency check failed"
	}
	return result
}

func (c *Checker) checkSupervisor(ctx context.Context) Dependency {
	started := time.Now()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.supervisorURL+"/health", nil)
	if err == nil {
		var response *http.Response
		response, err = c.client.Do(request)
		if response != nil {
			defer response.Body.Close()
			if response.StatusCode >= http.StatusBadRequest {
				err = fmt.Errorf("status %d", response.StatusCode)
			}
		}
	}
	result := Dependency{Status: "ok", LatencyMS: time.Since(started).Milliseconds()}
	if err != nil {
		result.Status = "failed"
		result.Error = "dependency check failed"
	}
	return result
}
