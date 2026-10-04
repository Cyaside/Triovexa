package modelgateway

import "github.com/Cyaside/Triovexa/internal/ai/admission"

// A named contract is part of the immutable pricing snapshot. Empty retains
// the existing administrator-verified byte/overhead contract for other models.
func validateInputContract(pricing admission.Pricing) error {
	if pricing.InputContract == "" {
		return nil
	}
	if pricing.InputContract != GLMFlashInputContract || pricing.Provider != "openai-compatible" || pricing.Model != "glm-5.3-flash" {
		return admission.ErrBillingUnbounded
	}
	return nil
}

func inputTokenBound(pricing admission.Pricing, body []byte) (int64, error) {
	if err := validateInputContract(pricing); err != nil {
		return 0, err
	}
	if pricing.InputContract == GLMFlashInputContract {
		bound, err := GLMFlashInputBound(body)
		if err != nil {
			return 0, admission.ErrBillingUnbounded
		}
		return bound, nil
	}
	return admission.ConservativeInputBound(body, 512)
}
