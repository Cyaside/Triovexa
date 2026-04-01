package telemetry

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type requestKey struct {
	Method string
	Route  string
	Status string
}

type routeKey struct {
	Method string
	Route  string
}

type triageStageKey struct {
	Stage  string
	Status string
}

type outcomeKey struct {
	Status string
}

type durationMetric struct {
	Count int64
	Sum   float64
}

type Recorder struct {
	mu sync.RWMutex

	startedAt time.Time

	incidentIntakeTotal int64

	httpRequests       map[requestKey]int64
	httpRequestLatency map[routeKey]durationMetric

	triageStageLatency map[triageStageKey]durationMetric
	policyDecisions    map[string]int64

	executionOutcomes map[outcomeKey]int64
	executionLatency  map[outcomeKey]durationMetric

	verificationOutcomes map[outcomeKey]int64
	verificationLatency  map[outcomeKey]durationMetric

	killSwitchEnabled bool
	killSwitchToggles int64
}

func NewRecorder() *Recorder {
	return &Recorder{
		startedAt:            time.Now().UTC(),
		httpRequests:         make(map[requestKey]int64),
		httpRequestLatency:   make(map[routeKey]durationMetric),
		triageStageLatency:   make(map[triageStageKey]durationMetric),
		policyDecisions:      make(map[string]int64),
		executionOutcomes:    make(map[outcomeKey]int64),
		executionLatency:     make(map[outcomeKey]durationMetric),
		verificationOutcomes: make(map[outcomeKey]int64),
		verificationLatency:  make(map[outcomeKey]durationMetric),
	}
}

func (r *Recorder) IncIncidentIngested() {
	if r == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.incidentIntakeTotal++
}

func (r *Recorder) ObserveHTTPRequest(method string, route string, status int, duration time.Duration) {
	if r == nil {
		return
	}

	statusLabel := strconv.Itoa(status)

	r.mu.Lock()
	defer r.mu.Unlock()

	requestMetricKey := requestKey{
		Method: method,
		Route:  route,
		Status: statusLabel,
	}
	r.httpRequests[requestMetricKey]++

	latencyKey := routeKey{
		Method: method,
		Route:  route,
	}
	current := r.httpRequestLatency[latencyKey]
	current.Count++
	current.Sum += duration.Seconds()
	r.httpRequestLatency[latencyKey] = current
}

func (r *Recorder) ObserveTriageStage(stage string, status string, duration time.Duration) {
	if r == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	key := triageStageKey{
		Stage:  stage,
		Status: status,
	}
	current := r.triageStageLatency[key]
	current.Count++
	current.Sum += duration.Seconds()
	r.triageStageLatency[key] = current
}

func (r *Recorder) IncPolicyDecision(decision string) {
	if r == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.policyDecisions[decision]++
}

func (r *Recorder) ObserveExecution(status string, duration time.Duration) {
	if r == nil {
		return
	}

	key := outcomeKey{Status: status}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.executionOutcomes[key]++
	current := r.executionLatency[key]
	current.Count++
	current.Sum += duration.Seconds()
	r.executionLatency[key] = current
}

func (r *Recorder) ObserveVerification(status string, duration time.Duration) {
	if r == nil {
		return
	}

	key := outcomeKey{Status: status}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.verificationOutcomes[key]++
	current := r.verificationLatency[key]
	current.Count++
	current.Sum += duration.Seconds()
	r.verificationLatency[key] = current
}

func (r *Recorder) RecordKillSwitchState(enabled bool) {
	if r == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.killSwitchEnabled != enabled {
		r.killSwitchToggles++
	}
	r.killSwitchEnabled = enabled
}

func (r *Recorder) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	if r == nil {
		http.Error(w, "telemetry recorder is not configured", http.StatusNotImplemented)
		return
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	lines := r.render()
	_, _ = w.Write([]byte(strings.Join(lines, "\n") + "\n"))
}

func (r *Recorder) render() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	lines := []string{
		"# HELP triovexa_uptime_seconds Process uptime in seconds.",
		"# TYPE triovexa_uptime_seconds gauge",
		fmt.Sprintf("triovexa_uptime_seconds %.6f", time.Since(r.startedAt).Seconds()),
		"# HELP triovexa_incident_intake_total Total incidents accepted from external alert intake.",
		"# TYPE triovexa_incident_intake_total counter",
		fmt.Sprintf("triovexa_incident_intake_total %d", r.incidentIntakeTotal),
		"# HELP triovexa_kill_switch_enabled Whether the global kill switch is enabled.",
		"# TYPE triovexa_kill_switch_enabled gauge",
		fmt.Sprintf("triovexa_kill_switch_enabled %d", boolToInt(r.killSwitchEnabled)),
		"# HELP triovexa_kill_switch_toggles_total Total kill switch state changes.",
		"# TYPE triovexa_kill_switch_toggles_total counter",
		fmt.Sprintf("triovexa_kill_switch_toggles_total %d", r.killSwitchToggles),
		"# HELP triovexa_http_requests_total Total HTTP requests handled by the operator service.",
		"# TYPE triovexa_http_requests_total counter",
	}

	requestKeys := make([]requestKey, 0, len(r.httpRequests))
	for key := range r.httpRequests {
		requestKeys = append(requestKeys, key)
	}
	sort.Slice(requestKeys, func(i, j int) bool {
		if requestKeys[i].Route != requestKeys[j].Route {
			return requestKeys[i].Route < requestKeys[j].Route
		}
		if requestKeys[i].Method != requestKeys[j].Method {
			return requestKeys[i].Method < requestKeys[j].Method
		}
		return requestKeys[i].Status < requestKeys[j].Status
	})
	for _, key := range requestKeys {
		lines = append(lines, fmt.Sprintf(
			`triovexa_http_requests_total{method=%q,route=%q,status=%q} %d`,
			key.Method,
			key.Route,
			key.Status,
			r.httpRequests[key],
		))
	}

	lines = append(lines,
		"# HELP triovexa_http_request_duration_seconds HTTP request duration summaries by route.",
		"# TYPE triovexa_http_request_duration_seconds summary",
	)
	httpLatencyKeys := make([]routeKey, 0, len(r.httpRequestLatency))
	for key := range r.httpRequestLatency {
		httpLatencyKeys = append(httpLatencyKeys, key)
	}
	sort.Slice(httpLatencyKeys, func(i, j int) bool {
		if httpLatencyKeys[i].Route != httpLatencyKeys[j].Route {
			return httpLatencyKeys[i].Route < httpLatencyKeys[j].Route
		}
		return httpLatencyKeys[i].Method < httpLatencyKeys[j].Method
	})
	for _, key := range httpLatencyKeys {
		metric := r.httpRequestLatency[key]
		lines = append(lines,
			fmt.Sprintf(`triovexa_http_request_duration_seconds_count{method=%q,route=%q} %d`, key.Method, key.Route, metric.Count),
			fmt.Sprintf(`triovexa_http_request_duration_seconds_sum{method=%q,route=%q} %.6f`, key.Method, key.Route, metric.Sum),
		)
	}

	lines = append(lines,
		"# HELP triovexa_triage_stage_duration_seconds Triage pipeline stage duration summaries.",
		"# TYPE triovexa_triage_stage_duration_seconds summary",
	)
	stageKeys := make([]triageStageKey, 0, len(r.triageStageLatency))
	for key := range r.triageStageLatency {
		stageKeys = append(stageKeys, key)
	}
	sort.Slice(stageKeys, func(i, j int) bool {
		if stageKeys[i].Stage != stageKeys[j].Stage {
			return stageKeys[i].Stage < stageKeys[j].Stage
		}
		return stageKeys[i].Status < stageKeys[j].Status
	})
	for _, key := range stageKeys {
		metric := r.triageStageLatency[key]
		lines = append(lines,
			fmt.Sprintf(`triovexa_triage_stage_duration_seconds_count{stage=%q,status=%q} %d`, key.Stage, key.Status, metric.Count),
			fmt.Sprintf(`triovexa_triage_stage_duration_seconds_sum{stage=%q,status=%q} %.6f`, key.Stage, key.Status, metric.Sum),
		)
	}

	lines = append(lines,
		"# HELP triovexa_policy_decisions_total Total policy decisions by outcome.",
		"# TYPE triovexa_policy_decisions_total counter",
	)
	decisions := make([]string, 0, len(r.policyDecisions))
	for decision := range r.policyDecisions {
		decisions = append(decisions, decision)
	}
	sort.Strings(decisions)
	for _, decision := range decisions {
		lines = append(lines, fmt.Sprintf(`triovexa_policy_decisions_total{decision=%q} %d`, decision, r.policyDecisions[decision]))
	}

	lines = append(lines,
		"# HELP triovexa_execution_outcomes_total Total execution outcomes by status.",
		"# TYPE triovexa_execution_outcomes_total counter",
	)
	executionKeys := make([]outcomeKey, 0, len(r.executionOutcomes))
	for key := range r.executionOutcomes {
		executionKeys = append(executionKeys, key)
	}
	sort.Slice(executionKeys, func(i, j int) bool {
		return executionKeys[i].Status < executionKeys[j].Status
	})
	for _, key := range executionKeys {
		lines = append(lines, fmt.Sprintf(`triovexa_execution_outcomes_total{status=%q} %d`, key.Status, r.executionOutcomes[key]))
	}

	lines = append(lines,
		"# HELP triovexa_execution_duration_seconds Execution duration summaries by outcome status.",
		"# TYPE triovexa_execution_duration_seconds summary",
	)
	for _, key := range executionKeys {
		metric := r.executionLatency[key]
		lines = append(lines,
			fmt.Sprintf(`triovexa_execution_duration_seconds_count{status=%q} %d`, key.Status, metric.Count),
			fmt.Sprintf(`triovexa_execution_duration_seconds_sum{status=%q} %.6f`, key.Status, metric.Sum),
		)
	}

	lines = append(lines,
		"# HELP triovexa_verification_outcomes_total Total verification outcomes by status.",
		"# TYPE triovexa_verification_outcomes_total counter",
	)
	verificationKeys := make([]outcomeKey, 0, len(r.verificationOutcomes))
	for key := range r.verificationOutcomes {
		verificationKeys = append(verificationKeys, key)
	}
	sort.Slice(verificationKeys, func(i, j int) bool {
		return verificationKeys[i].Status < verificationKeys[j].Status
	})
	for _, key := range verificationKeys {
		lines = append(lines, fmt.Sprintf(`triovexa_verification_outcomes_total{status=%q} %d`, key.Status, r.verificationOutcomes[key]))
	}

	lines = append(lines,
		"# HELP triovexa_verification_duration_seconds Verification duration summaries by outcome status.",
		"# TYPE triovexa_verification_duration_seconds summary",
	)
	for _, key := range verificationKeys {
		metric := r.verificationLatency[key]
		lines = append(lines,
			fmt.Sprintf(`triovexa_verification_duration_seconds_count{status=%q} %d`, key.Status, metric.Count),
			fmt.Sprintf(`triovexa_verification_duration_seconds_sum{status=%q} %.6f`, key.Status, metric.Sum),
		)
	}

	return lines
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
