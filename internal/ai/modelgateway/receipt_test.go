package modelgateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/ai/admission"
)

func TestInvalidBillingResponsesRetainReservationAndBlockReplay(t *testing.T) {
	for _, scenario := range []struct {
		name string
		body string
		want error
	}{
		{"model changed", `{"model":"different-model","usage":{"prompt_tokens":20,"completion_tokens":4,"total_tokens":24}}`, admission.ErrUsageInvalid},
		{"partial usage", `{"model":"fixture-model","usage":{"prompt_tokens":20}}`, admission.ErrUsageInvalid},
		{"fractional usage", `{"model":"fixture-model","usage":{"prompt_tokens":20.5,"completion_tokens":4,"total_tokens":24.5}}`, admission.ErrUsageInvalid},
		{"negative usage", `{"model":"fixture-model","usage":{"prompt_tokens":20,"completion_tokens":-1,"total_tokens":19}}`, admission.ErrUsageInvalid},
		{"incorrect total", `{"model":"fixture-model","usage":{"prompt_tokens":20,"completion_tokens":4,"total_tokens":25}}`, admission.ErrUsageInvalid},
		{"cached subset overflow", `{"model":"fixture-model","usage":{"prompt_tokens":20,"completion_tokens":4,"total_tokens":24,"prompt_tokens_details":{"cached_tokens":21}}}`, admission.ErrUsageInvalid},
		{"reasoning subset overflow", `{"model":"fixture-model","usage":{"prompt_tokens":20,"completion_tokens":4,"total_tokens":24,"completion_tokens_details":{"reasoning_tokens":5}}}`, admission.ErrUsageInvalid},
		{"input bound exceeded", `{"model":"fixture-model","usage":{"prompt_tokens":9999,"completion_tokens":4,"total_tokens":10003}}`, admission.ErrUsageInvalid},
		{"output bound exceeded", `{"model":"fixture-model","usage":{"prompt_tokens":20,"completion_tokens":1501,"total_tokens":1521}}`, admission.ErrUsageInvalid},
		{"ambiguous model", `{"model":"different-model","model":"fixture-model","usage":{"prompt_tokens":20,"completion_tokens":4,"total_tokens":24}}`, admission.ErrUsageInvalid},
		{"ambiguous usage", `{"model":"fixture-model","usage":{"prompt_tokens":9999,"prompt_tokens":20,"completion_tokens":4,"total_tokens":24}}`, admission.ErrUsageInvalid},
		{"malformed response JSON", `{"model":"fixture-model","usage":`, admission.ErrUsageInvalid},
		{"oversized response", strings.Repeat("a", maxProviderResponseBytes+1), admission.ErrUncertain},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var calls atomic.Int32
			gateway, ledger := gatewayFixture(t, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				fmt.Fprint(w, scenario.body)
			})
			gateway.config.Pricing.InputMicroUSDPerMillion = 1_000_000
			gateway.config.Pricing.OutputMicroUSDPerMillion = 1_000_000
			if _, _, _, err := gateway.DispatchAt(context.Background(), 1, modelPayload("test")); !errors.Is(err, scenario.want) {
				t.Fatalf("invalid billing response: %v", err)
			}
			for _, ordinal := range []int{1, 2} {
				if _, _, _, err := gateway.DispatchAt(context.Background(), ordinal, modelPayload("test")); !errors.Is(err, admission.ErrUncertain) {
					t.Fatalf("uncertain receipt replay at %d: %v", ordinal, err)
				}
			}
			campaign, _ := ledger.GetCampaign(context.Background(), gateway.config.CampaignID)
			request, _ := ledger.GetRequest(context.Background(), gateway.requestID(1))
			if calls.Load() != 1 || !campaign.Blocked || campaign.Requests != 1 || campaign.AdmittedInputTokens == 0 || campaign.ReservedMicroUSD == 0 || campaign.SpentMicroUSD != 0 || request.State != admission.Uncertain {
				t.Fatalf("unknown billing released reservation: calls=%d campaign=%+v state=%s", calls.Load(), campaign, request.State)
			}
		})
	}
}

func TestReceiptEnvelopePreservesExactBytesAndFitsDurableSizeLimit(t *testing.T) {
	for _, size := range []int{0, maxProviderResponseBytes - len(billedFixtureResponse) - 3} {
		t.Run(fmt.Sprintf("padding-%d", size), func(t *testing.T) {
			// Whitespace is legitimate JSON response padding and must not change
			// on receipt replay, even though the credential cipher trims text.
			body := " \n" + billedFixtureResponse + strings.Repeat(" ", size) + "\n"
			var calls atomic.Int32
			gateway, ledger := gatewayFixture(t, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				fmt.Fprint(w, body)
			})
			for replay := 0; replay < 2; replay++ {
				response, status, _, err := gateway.DispatchAt(context.Background(), 1, modelPayload("test"))
				if err != nil || status != http.StatusOK || string(response) != body {
					t.Fatalf("response bytes changed on replay: size=%d status=%d err=%v", len(response), status, err)
				}
			}
			request, _ := ledger.GetRequest(context.Background(), gateway.requestID(1))
			if calls.Load() != 1 || request.State != admission.Accounted || len(request.Receipt.Response) > 256*1024 {
				t.Fatal("bounded envelope did not fit the durable receipt or caused duplicate dispatch")
			}
		})
	}
}

func TestEmptyProviderResponseStillRetainsHTTPStatus(t *testing.T) {
	for _, status := range []int{http.StatusNoContent, http.StatusUnauthorized, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprintf("http-%d", status), func(t *testing.T) {
			gateway, ledger := gatewayFixture(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) })
			if _, _, _, err := gateway.DispatchAt(context.Background(), 1, modelPayload("test")); !errors.Is(err, admission.ErrUsageInvalid) {
				t.Fatalf("empty response: %v", err)
			}
			request, _ := ledger.GetRequest(context.Background(), gateway.requestID(1))
			if request.State != admission.Uncertain || request.Receipt == nil || request.Receipt.HTTPStatus != status || request.Receipt.ResponseEncoding != "sealed-v1" {
				t.Fatal("empty response lost HTTP status or released uncertain reservation")
			}
		})
	}
}

func TestProviderTimeoutNeverRetriesAndBlocksNextCall(t *testing.T) {
	var calls atomic.Int32
	gateway, ledger := gatewayFixture(t, func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		calls.Add(1)
		<-r.Context().Done()
	})
	gateway.client.Timeout = 25 * time.Millisecond
	gateway.config.Pricing.InputMicroUSDPerMillion = 1_000_000
	gateway.config.Pricing.OutputMicroUSDPerMillion = 1_000_000
	if _, _, _, err := gateway.DispatchAt(context.Background(), 1, modelPayload("test")); !errors.Is(err, admission.ErrUncertain) {
		t.Fatalf("timeout: %v", err)
	}
	if _, _, _, err := gateway.DispatchAt(context.Background(), 2, modelPayload("test")); !errors.Is(err, admission.ErrUncertain) || calls.Load() != 1 {
		t.Fatal("timed-out provider was retried")
	}
	campaign, _ := ledger.GetCampaign(context.Background(), gateway.config.CampaignID)
	request, _ := ledger.GetRequest(context.Background(), gateway.requestID(1))
	if !campaign.Blocked || campaign.ReservedMicroUSD == 0 || campaign.SpentMicroUSD != 0 || request.State != admission.Uncertain || request.Receipt != nil {
		t.Fatal("unknown timeout was converted into a fabricated response")
	}
}

func TestCachedAndReasoningTokensAreSubsetsAndMalformedContentStillBilled(t *testing.T) {
	gateway, ledger := gatewayFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"model":"fixture-model","choices":"not a valid native completion","usage":{"prompt_tokens":20,"completion_tokens":4,"total_tokens":24,"prompt_tokens_details":{"cached_tokens":12},"completion_tokens_details":{"reasoning_tokens":3}}}`)
	})
	// Non-zero synthetic prices distinguish total billing from double-counting
	// details. This is a loopback fixture, never a real provider rate claim.
	gateway.config.Pricing.InputMicroUSDPerMillion = 1_000_000
	gateway.config.Pricing.OutputMicroUSDPerMillion = 1_000_000
	if _, status, _, err := gateway.DispatchAt(context.Background(), 1, modelPayload("test")); err != nil || status != http.StatusOK {
		t.Fatalf("billing receipt: status=%d err=%v", status, err)
	}
	request, _ := ledger.GetRequest(context.Background(), gateway.requestID(1))
	campaign, _ := ledger.GetCampaign(context.Background(), gateway.config.CampaignID)
	if request.State != admission.Accounted || request.Receipt.Usage.CachedInputTokens != 12 || request.Receipt.Usage.ReasoningTokens != 3 || campaign.SpentMicroUSD != 24 || campaign.ReservedMicroUSD != 0 {
		t.Fatalf("billing details double-counted or discarded: request=%+v campaign=%+v", request, campaign)
	}
}
