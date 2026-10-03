package modelgateway

import (
	"encoding/json"
	"errors"
)

// A stage can narrow the product inventory; it can never add model authority.
func validateStageTools(body []byte, inventory []string) error {
	if inventory == nil {
		return nil
	}
	allowed := make(map[string]bool, len(inventory))
	for _, name := range inventory {
		allowed[name] = true
	}
	var payload struct {
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
		Messages []struct {
			ToolCalls []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return errors.New("PROVIDER_CONTRACT_INVALID")
	}
	for _, tool := range payload.Tools {
		if !allowed[tool.Function.Name] {
			return errors.New("TOOL_SCOPE_DENIED")
		}
	}
	for _, message := range payload.Messages {
		for _, call := range message.ToolCalls {
			if !allowed[call.Function.Name] {
				return errors.New("TOOL_SCOPE_DENIED")
			}
		}
	}
	return nil
}
