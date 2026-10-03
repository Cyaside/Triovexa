package modelgateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

var allowedTools = map[string]bool{"repo_list": true, "repo_read": true, "repo_search": true,
	"run_test_recipe": true, "propose_patch": true, "cannot_determine": true, "read_file": true}

// ValidatePayload runs on the final SDK wire body, including middleware and
// tool schemas. It accepts only the non-streaming Chat Completions contract.
func ValidatePayload(body []byte, model string, maxOutput int) error {
	var fields map[string]json.RawMessage
	if len(body) == 0 || len(body) > 256*1024 || unambiguousJSON(body) != nil || json.Unmarshal(body, &fields) != nil {
		return errors.New("PROVIDER_CONTRACT_INVALID: model payload is not a bounded object")
	}
	allowed := map[string]bool{"model": true, "messages": true, "tools": true, "tool_choice": true,
		"parallel_tool_calls": true, "max_tokens": true, "temperature": true, "stream": true, "response_format": true}
	for key := range fields {
		if !allowed[key] {
			return fmt.Errorf("PROVIDER_CONTRACT_INVALID: unsupported request field %q", key)
		}
	}
	var requestedModel string
	if json.Unmarshal(fields["model"], &requestedModel) != nil || requestedModel != model {
		return errors.New("PROVIDER_CHANGED: model does not match the pinned configuration")
	}
	var output int
	if json.Unmarshal(fields["max_tokens"], &output) != nil || output < 1 || output > maxOutput {
		return errors.New("BILLING_UNBOUNDED: request requires a verified output token cap")
	}
	var stream bool
	if value := fields["stream"]; value != nil && (json.Unmarshal(value, &stream) != nil || stream) {
		return errors.New("PROVIDER_CONTRACT_INVALID: streaming is not supported")
	}
	var parallel bool
	if value := fields["parallel_tool_calls"]; value != nil && (json.Unmarshal(value, &parallel) != nil || parallel) {
		return errors.New("PROVIDER_CONTRACT_INVALID: parallel tool dispatch is disabled")
	}
	var messages []map[string]json.RawMessage
	if json.Unmarshal(fields["messages"], &messages) != nil || len(messages) < 1 || len(messages) > 100 {
		return errors.New("PROVIDER_CONTRACT_INVALID: messages are missing or unbounded")
	}
	pending := make(map[string]bool)
	seen := make(map[string]bool)
	for _, message := range messages {
		for key := range message {
			if key != "role" && key != "content" && key != "tool_calls" && key != "tool_call_id" && key != "reasoning_content" && key != "name" {
				return errors.New("PROVIDER_CONTRACT_INVALID: unsupported message field")
			}
		}
		var role string
		if json.Unmarshal(message["role"], &role) != nil {
			return errors.New("PROVIDER_CONTRACT_INVALID: missing message role")
		}
		if value := message["content"]; value != nil && string(value) != "null" {
			var text string
			if json.Unmarshal(value, &text) != nil {
				var blocks []map[string]json.RawMessage
				if json.Unmarshal(value, &blocks) != nil || len(blocks) > 100 {
					return errors.New("PROVIDER_CONTRACT_INVALID: multimodal content is not supported")
				}
				for _, block := range blocks {
					var kind, content string
					if len(block) != 2 || json.Unmarshal(block["type"], &kind) != nil || kind != "text" || json.Unmarshal(block["text"], &content) != nil {
						return errors.New("PROVIDER_CONTRACT_INVALID: only text message blocks are supported")
					}
				}
			}
		}
		if role == "tool" {
			var id string
			if json.Unmarshal(message["tool_call_id"], &id) != nil || !pending[id] {
				return errors.New("PROVIDER_CONTRACT_INVALID: tool result has no matching call")
			}
			delete(pending, id)
			continue
		}
		if len(pending) != 0 {
			return errors.New("PROVIDER_CONTRACT_INVALID: missing tool result")
		}
		if role != "system" && role != "user" && role != "assistant" {
			return errors.New("PROVIDER_CONTRACT_INVALID: unsupported role")
		}
		if calls := message["tool_calls"]; calls != nil {
			if role != "assistant" {
				return errors.New("PROVIDER_CONTRACT_INVALID: calls require assistant role")
			}
			var definitions []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			}
			if json.Unmarshal(calls, &definitions) != nil || len(definitions) > 7 {
				return errors.New("PROVIDER_CONTRACT_INVALID: tool calls are invalid")
			}
			for _, call := range definitions {
				var args map[string]any
				if call.ID == "" || len(call.ID) > 128 || seen[call.ID] || call.Type != "function" || !allowedTools[call.Function.Name] || unambiguousJSON([]byte(call.Function.Arguments)) != nil || json.Unmarshal([]byte(call.Function.Arguments), &args) != nil || args == nil {
					return errors.New("PROVIDER_CONTRACT_INVALID: duplicate ID, unknown tool or invalid arguments")
				}
				seen[call.ID], pending[call.ID] = true, true
			}
		}
	}
	if len(pending) != 0 {
		return errors.New("PROVIDER_CONTRACT_INVALID: conversation ends with unanswered tools")
	}
	if tools := fields["tools"]; tools != nil {
		var definitions []struct {
			Type     string `json:"type"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		}
		if json.Unmarshal(tools, &definitions) != nil || len(definitions) > 7 {
			return errors.New("PROVIDER_CONTRACT_INVALID: tool inventory exceeds profile")
		}
		seenNames := make(map[string]bool)
		for _, definition := range definitions {
			if definition.Type != "function" || !allowedTools[definition.Function.Name] || seenNames[definition.Function.Name] {
				return errors.New("PROVIDER_CONTRACT_INVALID: tool inventory is not allowlisted")
			}
			seenNames[definition.Function.Name] = true
		}
	}
	return nil
}

// Different upstream JSON parsers may resolve duplicate keys differently. A
// pinned model, output cap or native call must have one unambiguous meaning.
func unambiguousJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := uniqueJSONValue(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("invalid JSON remainder")
	}
	return nil
}

func uniqueJSONValue(decoder *json.Decoder, depth int) error {
	if depth > 64 {
		return errors.New("JSON nesting exceeds its bound")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, container := token.(json.Delim)
	if !container {
		return nil
	}
	if delimiter != '{' && delimiter != '[' {
		return errors.New("invalid JSON delimiter")
	}
	seen := make(map[string]bool)
	for decoder.More() {
		if delimiter == '{' {
			key, err := decoder.Token()
			name, ok := key.(string)
			if err != nil || !ok || seen[name] {
				return errors.New("duplicate or invalid JSON object key")
			}
			seen[name] = true
		}
		if err := uniqueJSONValue(decoder, depth+1); err != nil {
			return err
		}
	}
	closing, err := decoder.Token()
	if err != nil || (delimiter == '{' && closing != json.Delim('}')) || (delimiter == '[' && closing != json.Delim(']')) {
		return errors.New("invalid JSON closing delimiter")
	}
	return nil
}

func completionURL(baseURL string) string {
	root := strings.TrimRight(baseURL, "/")
	if strings.HasSuffix(strings.ToLower(root), "/v1") {
		return root + "/chat/completions"
	}
	return root + "/v1/chat/completions"
}
