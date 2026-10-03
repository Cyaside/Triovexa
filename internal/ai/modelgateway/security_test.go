package modelgateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/ai/admission"
)

const billedFixtureResponse = `{"model":"fixture-model","choices":[],"usage":{"prompt_tokens":20,"completion_tokens":4,"total_tokens":24}}`

func privateRequest(gateway *Gateway, body []byte) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
	request.Header.Set("Authorization", "Bearer "+gateway.Capability())
	request.Header.Set("X-Triovexa-Case-ID", gateway.config.CaseID)
	request.Header.Set("X-Triovexa-Attempt-ID", gateway.config.AttemptID)
	request.Header.Set("X-Triovexa-Request-Ordinal", "1")
	return request
}

func TestPrivateCapabilityRejectsCrossScopeAndBrowserAccessBeforeAdmission(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		modify func(*http.Request)
		status int
	}{
		{"missing capability", func(r *http.Request) { r.Header.Del("Authorization") }, http.StatusForbidden},
		{"wrong capability", func(r *http.Request) { r.Header.Set("Authorization", "Bearer synthetic-wrong-capability") }, http.StatusForbidden},
		{"wrong case", func(r *http.Request) { r.Header.Set("X-Triovexa-Case-ID", "another-case") }, http.StatusForbidden},
		{"wrong attempt", func(r *http.Request) { r.Header.Set("X-Triovexa-Attempt-ID", "another-attempt") }, http.StatusForbidden},
		{"browser origin", func(r *http.Request) { r.Header.Set("Origin", "http://127.0.0.1:8080") }, http.StatusForbidden},
		{"null origin", func(r *http.Request) { r.Header.Set("Origin", "null") }, http.StatusForbidden},
		{"ordinal skip", func(r *http.Request) { r.Header.Set("X-Triovexa-Request-Ordinal", "2") }, http.StatusConflict},
		{"ordinal zero", func(r *http.Request) { r.Header.Set("X-Triovexa-Request-Ordinal", "0") }, http.StatusConflict},
		{"ordinal overflow", func(r *http.Request) { r.Header.Set("X-Triovexa-Request-Ordinal", "5") }, http.StatusConflict},
		{"query override", func(r *http.Request) { r.URL.RawQuery = "model=expensive-model" }, http.StatusNotFound},
		{"alternate route", func(r *http.Request) { r.URL.Path = "/v1/responses" }, http.StatusNotFound},
		{"alternate method", func(r *http.Request) { r.Method = http.MethodGet }, http.StatusNotFound},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			gateway, ledger := gatewayFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("unauthorized request reached upstream") })
			request := privateRequest(gateway, modelPayload("test"))
			scenario.modify(request)
			response := httptest.NewRecorder()
			gateway.ServeHTTP(response, request)
			campaign, _ := ledger.GetCampaign(context.Background(), gateway.config.CampaignID)
			if response.Code != scenario.status || campaign.Requests != 0 {
				t.Fatalf("private rejection: status=%d requests=%d", response.Code, campaign.Requests)
			}
		})
	}
}

func TestPrivatePayloadReadIsBoundedBeforeReservation(t *testing.T) {
	gateway, ledger := gatewayFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("oversized payload reached upstream") })
	response := httptest.NewRecorder()
	gateway.ServeHTTP(response, privateRequest(gateway, []byte(strings.Repeat("a", 256*1024+1))))
	campaign, _ := ledger.GetCampaign(context.Background(), gateway.config.CampaignID)
	if response.Code != http.StatusRequestEntityTooLarge || campaign.Requests != 0 {
		t.Fatalf("oversized body: status=%d requests=%d", response.Code, campaign.Requests)
	}
}

func TestConcurrentReceiptReplayDispatchesExactlyOnce(t *testing.T) {
	var calls atomic.Int32
	gateway, ledger := gatewayFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, billedFixtureResponse)
	})
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body, status, _, err := gateway.DispatchAt(context.Background(), 1, modelPayload("same request"))
			if err != nil || status != http.StatusOK || string(body) != billedFixtureResponse {
				t.Errorf("receipt replay: status=%d err=%v", status, err)
			}
		}()
	}
	wg.Wait()
	campaign, _ := ledger.GetCampaign(context.Background(), gateway.config.CampaignID)
	if calls.Load() != 1 || campaign.Requests != 1 || campaign.Blocked {
		t.Fatalf("parallel replay: calls=%d campaign=%+v", calls.Load(), campaign)
	}
}

func TestLeaseFencesBothReservationAndExternalDispatch(t *testing.T) {
	for _, lossAt := range []int32{1, 2} {
		t.Run(fmt.Sprintf("fence-%d", lossAt), func(t *testing.T) {
			var checks atomic.Int32
			gateway, ledger := gatewayFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("lost lease reached provider") })
			gateway.config.Fence = func(context.Context) error {
				if checks.Add(1) >= lossAt {
					return errors.New("synthetic lease lost")
				}
				return nil
			}
			if _, _, _, err := gateway.DispatchAt(context.Background(), 1, modelPayload("test")); err == nil || err.Error() != "LEASE_LOST" {
				t.Fatalf("lease loss: %v", err)
			}
			campaign, _ := ledger.GetCampaign(context.Background(), gateway.config.CampaignID)
			if campaign.Requests != 0 || campaign.AdmittedInputTokens != 0 || campaign.ReservedMicroUSD != 0 {
				t.Fatal("never-dispatched cancellation consumed allowance")
			}
			if lossAt == 2 {
				request, err := ledger.GetRequest(context.Background(), gateway.requestID(1))
				if err != nil || request.State != admission.Cancelled {
					t.Fatal("pre-dispatch lease loss did not cancel reservation")
				}
			}
		})
	}
}

func TestProviderFailureSealsStatusAndNeverRelaysPrivateResponse(t *testing.T) {
	const secret = "private-unlabelled-provider-material-f94a0b"
	var calls atomic.Int32
	gateway, ledger := gatewayFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("X-Private-Provider-Header", secret)
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, `{"model":"fixture-model","error":"%s","usage":{"prompt_tokens":20,"completion_tokens":4,"total_tokens":24}}`, secret)
	})
	response := httptest.NewRecorder()
	gateway.ServeHTTP(response, privateRequest(gateway, modelPayload("test")))
	if response.Code != http.StatusConflict || strings.Contains(response.Body.String(), secret) || response.Header().Get("X-Private-Provider-Header") != "" {
		t.Fatal("private provider error or headers reached caller")
	}
	request, err := ledger.GetRequest(context.Background(), gateway.requestID(1))
	if err != nil || request.State != admission.Uncertain || request.Receipt == nil || request.Receipt.HTTPStatus != http.StatusUnauthorized || request.Receipt.Usage.Present || strings.Contains(string(request.Receipt.Response), secret) {
		t.Fatal("failed HTTP response was accepted as success or persisted in plaintext")
	}
	if _, _, _, err := gateway.DispatchAt(context.Background(), 2, modelPayload("new")); !errors.Is(err, admission.ErrUncertain) || calls.Load() != 1 {
		t.Fatal("provider failure permitted a follow-up request")
	}
}

func TestProviderRedirectIsNotFollowed(t *testing.T) {
	var leaked atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked.Add(1) }))
	defer destination.Close()
	gateway, ledger := gatewayFixture(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	})
	if _, _, _, err := gateway.DispatchAt(context.Background(), 1, modelPayload("test")); !errors.Is(err, admission.ErrUsageInvalid) {
		t.Fatalf("redirect receipt: %v", err)
	}
	request, _ := ledger.GetRequest(context.Background(), gateway.requestID(1))
	if leaked.Load() != 0 || request.State != admission.Uncertain || request.Receipt.HTTPStatus != http.StatusTemporaryRedirect {
		t.Fatal("provider redirect followed or HTTP status lost")
	}
}

func TestStopCancelsInflightGatewayAndIsIdempotent(t *testing.T) {
	entered, cancelled := make(chan struct{}), make(chan struct{})
	gateway, ledger := gatewayFixture(t, func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(entered)
		<-r.Context().Done()
		close(cancelled)
	})
	base, stop, err := gateway.Listen(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	request := privateRequest(gateway, modelPayload("test"))
	request.URL, err = url.Parse(base + "/chat/completions")
	if err != nil {
		t.Fatal(err)
	}
	request.RequestURI = ""
	done := make(chan struct{})
	go func() {
		defer close(done)
		response, err := (&http.Client{Timeout: 3 * time.Second}).Do(request)
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("fixture did not receive dispatch")
	}
	stop()
	stop()
	for _, signal := range []<-chan struct{}{cancelled, done} {
		select {
		case <-signal:
		case <-time.After(time.Second):
			t.Fatal("gateway cleanup retained an active request or watcher")
		}
	}
	// The upstream cancellation and dispatch goroutine can finish independently.
	deadline := time.Now().Add(time.Second)
	for {
		record, _ := ledger.GetRequest(context.Background(), gateway.requestID(1))
		if record.State == admission.Uncertain {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cancelled external dispatch did not retain uncertain reservation")
		}
		time.Sleep(time.Millisecond)
	}
}
