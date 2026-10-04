package modelgateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Cyaside/Triovexa/internal/ai/admission"
)

func TestGLMInputContractIsPinnedBeforeDispatch(t *testing.T) {
	pricing := admission.Pricing{Provider: "openai-compatible", Model: "glm-5.3-flash", InputContract: GLMFlashInputContract}
	if err := validateInputContract(pricing); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*admission.Pricing){
		func(p *admission.Pricing) { p.Model = "different-model" },
		func(p *admission.Pricing) { p.Provider = "different-provider" },
		func(p *admission.Pricing) { p.InputContract = "unrecognized-template" },
	} {
		changed := pricing
		mutate(&changed)
		gateway, ledger := gatewayFixture(t, func(http.ResponseWriter, *http.Request) { t.Fatal("contract mismatch dispatched") })
		config := gateway.config
		config.Pricing = changed
		if _, err := New(config, ledger); !errors.Is(err, admission.ErrBillingUnbounded) {
			t.Fatalf("gateway accepted mismatched input contract: %v", err)
		}
		budget := BudgetConfig{Pricing: changed}
		if !errors.Is(budget.Validate(), admission.ErrBillingUnbounded) {
			t.Fatal("budget accepted mismatched input contract")
		}
	}
}

func TestGLMRenderedInputBoundReachesLedger(t *testing.T) {
	gateway, ledger := gatewayFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"model":"glm-5.3-flash","choices":[],"usage":{"prompt_tokens":20,"completion_tokens":4,"total_tokens":24}}`)
	})
	gateway.config.Provider.Model = "glm-5.3-flash"
	gateway.config.Pricing.Model = "glm-5.3-flash"
	gateway.config.Pricing.InputContract = GLMFlashInputContract
	body := []byte(strings.ReplaceAll(string(modelPayload("hello")), "fixture-model", "glm-5.3-flash"))
	bound, err := GLMFlashInputBound(body)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := gateway.DispatchAt(context.Background(), 1, body); err != nil {
		t.Fatal(err)
	}
	stored, err := ledger.GetRequest(context.Background(), gateway.requestID(1))
	if err != nil || stored.Request.InputTokenBound != bound || stored.Pricing.InputContract != GLMFlashInputContract {
		t.Fatalf("rendered bound or immutable contract missing: %+v %v", stored, err)
	}
}

func TestGLMInputAdmissionCountsRenderedEscapesAndRejectsOversize(t *testing.T) {
	calls := 0
	gateway, _ := gatewayFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		fmt.Fprint(w, `{"model":"glm-5.3-flash","choices":[],"usage":{"prompt_tokens":20,"completion_tokens":4,"total_tokens":24}}`)
	})
	gateway.config.Provider.Model = "glm-5.3-flash"
	gateway.config.Pricing.Model = "glm-5.3-flash"
	gateway.config.Pricing.InputContract = GLMFlashInputContract
	body := []byte(`{"model":"glm-5.3-flash","messages":[{"role":"user","content":"` + strings.Repeat(`\u0061`, 1000) + `"}],"max_tokens":1500}`)
	if len(body) <= 6000 {
		t.Fatal("fixture must distinguish wire bytes from rendered input")
	}
	if _, _, _, err := gateway.DispatchAt(context.Background(), 1, body); err != nil || calls != 1 {
		t.Fatalf("JSON escaping was incorrectly billed as rendered content: %v", err)
	}
	tooLarge := []byte(strings.ReplaceAll(string(modelPayload(strings.Repeat("x", 6000))), "fixture-model", "glm-5.3-flash"))
	if _, _, _, err := gateway.DispatchAt(context.Background(), 2, tooLarge); err == nil || calls != 1 {
		t.Fatal("oversize rendered input reached the provider")
	}
}
