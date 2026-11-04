package verification

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/Cyaside/Triovexa/internal/coderepair"
)

type HTTPQueries struct{ Requests, Health, Revision string }

// RenderHTTPQueries accepts only validated metric names and literal labels.
// Raw sample freshness is checked inside PromQL, not inferred from query time.
func RenderHTTPQueries(contract coderepair.TelemetryContract) (HTTPQueries, error) {
	if err := contract.Validate(); err != nil {
		return HTTPQueries{}, err
	}
	h := contract.Profile.HTTP
	if h == nil {
		return HTTPQueries{}, fmt.Errorf("HTTP signals are required")
	}
	labels := map[string]string{h.ServiceLabel: contract.ServiceName, h.EnvironmentLabel: contract.Environment}
	selector := func(metric string, extra map[string]string) string {
		all := map[string]string{}
		for k, v := range labels {
			all[k] = v
		}
		for k, v := range extra {
			all[k] = v
		}
		keys := make([]string, 0, len(all))
		for k := range all {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+"="+strconv.Quote(all[k]))
		}
		return metric + "{" + strings.Join(parts, ",") + "}"
	}
	fresh := func(s string) string {
		return "((time() - timestamp(" + s + ")) <= 60) and ((time() - timestamp(" + s + ")) >= -5)"
	}
	r := selector(h.RequestsMetric, h.RequestLabels)
	health, revision := selector(h.HealthMetric, nil), selector(h.RevisionMetric, nil)
	return HTTPQueries{
		Requests: fmt.Sprintf("sum by (%s,%s,%s) (increase(%s[%ds]) and (%s))", h.ServiceLabel, h.EnvironmentLabel, h.StatusLabel, r, h.WindowSeconds, fresh(r)),
		Health:   "(" + health + ") and (" + fresh(health) + ")",
		Revision: "(" + revision + ") and (" + fresh(revision) + ")",
	}, nil
}
