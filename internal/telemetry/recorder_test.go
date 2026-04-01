package telemetry

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRecorderInitialKillSwitchStateDoesNotCountAsToggle(t *testing.T) {
	t.Parallel()

	recorder := NewRecorder()
	recorder.RecordKillSwitchState(true)

	response := httptest.NewRecorder()
	recorder.ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	body := response.Body.String()

	if !strings.Contains(body, "triovexa_kill_switch_enabled 1") {
		t.Fatalf("metrics should expose kill switch as enabled, got:\n%s", body)
	}
	if !strings.Contains(body, "triovexa_kill_switch_toggles_total 0") {
		t.Fatalf("initial kill switch observation should not count as toggle, got:\n%s", body)
	}
}

func TestRecorderCountsSubsequentKillSwitchTransitions(t *testing.T) {
	t.Parallel()

	recorder := NewRecorder()
	recorder.RecordKillSwitchState(false)
	recorder.RecordKillSwitchState(true)
	recorder.RecordKillSwitchState(true)
	recorder.RecordKillSwitchState(false)

	response := httptest.NewRecorder()
	recorder.ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	body := response.Body.String()

	if !strings.Contains(body, "triovexa_kill_switch_enabled 0") {
		t.Fatalf("metrics should expose kill switch as disabled after final transition, got:\n%s", body)
	}
	if !strings.Contains(body, "triovexa_kill_switch_toggles_total 2") {
		t.Fatalf("kill switch counter should track only actual transitions, got:\n%s", body)
	}
}
