package http

import (
	"errors"
	"net/http"

	"github.com/Cyaside/Triovexa/internal/ai/admission"
)

func writeReasoningTestError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, admission.ErrPricingUnknown), errors.Is(err, admission.ErrBillingUnbounded):
		writeAPIError(w, http.StatusServiceUnavailable, "model_admission_unavailable", "Model requests are blocked until pricing and billing limits are verified.")
	case errors.Is(err, admission.ErrBudgetExceeded):
		writeAPIError(w, http.StatusTooManyRequests, "model_budget_exhausted", "The shared model budget is exhausted.")
	case errors.Is(err, admission.ErrUncertain), errors.Is(err, admission.ErrAlreadyDispatched):
		writeAPIError(w, http.StatusConflict, "model_dispatch_uncertain", "A previous model request must be reconciled before another request can be sent.")
	case errors.Is(err, admission.ErrOffline):
		writeAPIError(w, http.StatusForbidden, "offline_model_restricted", "The offline profile only permits local test providers.")
	default:
		writeAPIError(w, http.StatusBadGateway, "provider_test_failed", "The provider did not return a valid JSON response. Check the server logs and provider settings.")
	}
}
