package modelgateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

// GLMFlashInputContract identifies the text/function-tools template used by this
// bound. Model aliases or gateways using a different template need a new contract.
const GLMFlashInputContract = "glm-5.3-flash-template-690b705-v1"

// Upstream: https://huggingface.co/zai-org/GLM-5.3-Flash/tree/690b705278a3a58e538fcb37c2ca8b5f9511213c
// chat_template.jinja SHA256: 0c4099f3382d6c92700dfb99725025360966fd73032f0ecf32377c0d9e6309c5
// tokenizer.json SHA256: 19e773648cb4e65de8660ea6365e10acca112d42a854923df93db4a6f333a82d
// The tokenizer has no normalizer, ByteLevel(add_prefix_space=false), and BPE
// without subword prefixes/suffixes. Splitting/merging cannot create more tokens
// than UTF-8 bytes; added tokens only reduce this bound. Count rendered template
// bytes, not HTTP JSON bytes. This proves the published tokenizer/template only;
// the caller must independently verify its gateway uses this input contract.

const glmToolPrefix = "<|system|>\n# Tools\n\nYou may call one or more functions to assist with the user query.\n\nYou are provided with function signatures within <tools></tools> XML tags:\n<tools>\n"
const glmToolSuffix = "\n</tools>\n\nFor each function call, output the function name and arguments within the following XML format:\n<tool_call>{function-name}<arg_key>{arg-key-1}</arg_key><arg_value>{arg-value-1}</arg_value><arg_key>{arg-key-2}</arg_key><arg_value>{arg-value-2}</arg_value>...</tool_call>"

var errGLMInputShape = errors.New("BILLING_UNBOUNDED: unsupported GLM template input shape")

// GLMFlashInputBound bounds all text inserted by the pinned chat template,
// including native tool definitions, thinking history, and XML tool arguments.
// ValidatePayload remains the caller's authority/policy check. This function
// rejects shape ambiguities rather than assuming an upstream rendering rule.
func GLMFlashInputBound(body []byte) (int64, error) {
	if len(body) == 0 || len(body) > 256*1024 || !utf8.Valid(body) || unambiguousJSON(body) != nil {
		return 0, errGLMInputShape
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var request map[string]any
	if decoder.Decode(&request) != nil || request == nil {
		return 0, errGLMInputShape
	}
	for key := range request {
		switch key {
		case "model", "messages", "tools", "tool_choice", "parallel_tool_calls", "max_tokens", "temperature", "stream", "response_format":
		default:
			return 0, errGLMInputShape
		}
	}
	messages, ok := request["messages"].([]any)
	if !ok || len(messages) == 0 || len(messages) > 100 {
		return 0, errGLMInputShape
	}
	// The generation prefix and default reasoning-effort message are unconditional.
	bound := len("[gMASK]<sop><|system|>Reasoning Effort: Max<|assistant|><think>")
	if value, exists := request["tools"]; exists {
		tools, ok := value.([]any)
		if !ok || len(tools) > 7 {
			return 0, errGLMInputShape
		}
		if len(tools) > 0 {
			bound += len(glmToolPrefix) + len(glmToolSuffix)
		}
		for _, value := range tools {
			tool, ok := value.(map[string]any)
			if !ok || len(tool) != 2 || tool["type"] != "function" {
				return 0, errGLMInputShape
			}
			function, ok := tool["function"].(map[string]any)
			if !ok {
				return 0, errGLMInputShape
			}
			name, ok := function["name"].(string)
			if !ok || !allowedTools[name] {
				return 0, errGLMInputShape
			}
			visible := make(map[string]any)
			for key, value := range function {
				switch key {
				case "name", "description", "parameters":
					visible[key] = value
				case "strict": // tool_to_json explicitly omits strict.
					if _, ok := value.(bool); !ok {
						return 0, errGLMInputShape
					}
				case "defer_loading":
					if value != false {
						return 0, errGLMInputShape
					}
				default:
					return 0, errGLMInputShape
				}
			}
			size, err := glmJSONBytes(visible)
			if err != nil {
				return 0, err
			}
			// After explicit Jinja '-' whitespace stripping the loop retains three
			// literal LFs: before/after tool_to_json and after its endif. The suffix
			// includes the LF after endfor. HF trim_blocks removes some of these;
			// counting all four also bounds a renderer without implicit trimming.
			bound += size + 3
		}
	}
	for _, value := range messages {
		message, ok := value.(map[string]any)
		if !ok {
			return 0, errGLMInputShape
		}
		for key := range message {
			switch key {
			case "role", "content", "reasoning_content", "tool_calls", "tool_call_id", "name":
			default:
				return 0, errGLMInputShape
			}
		}
		content, err := glmContentBytes(message["content"])
		if err != nil {
			return 0, err
		}
		role, ok := message["role"].(string)
		if !ok {
			return 0, errGLMInputShape
		}
		switch role {
		case "system", "user":
			bound += len("<|"+role+"|>") + content
		case "assistant":
			bound += len("<|assistant|><think></think>") + content
			if value := message["reasoning_content"]; value != nil {
				thinking, ok := value.(string)
				if !ok {
					return 0, errGLMInputShape
				}
				bound += len(thinking)
			}
			if value, exists := message["tool_calls"]; exists {
				size, err := glmCallBytes(value)
				if err != nil {
					return 0, err
				}
				bound += size
			}
		case "tool":
			// Counting observation for every result overcounts consecutive blocks.
			bound += len("<|observation|><tool_response></tool_response>") + content
		default:
			return 0, errGLMInputShape
		}
		// At most two literal LFs occur in the assistant message control branches.
		// Counting them for every role also covers the other message branches.
		bound += 2
	}
	return int64(bound), nil
}

func glmContentBytes(value any) (int, error) {
	if value == nil {
		return 0, nil
	}
	if text, ok := value.(string); ok {
		return len(text), nil
	}
	blocks, ok := value.([]any)
	if !ok || len(blocks) > 100 {
		return 0, errGLMInputShape
	}
	size := 0
	for _, value := range blocks {
		block, ok := value.(map[string]any)
		if !ok || len(block) != 2 || block["type"] != "text" {
			return 0, errGLMInputShape
		}
		text, ok := block["text"].(string)
		if !ok {
			return 0, errGLMInputShape
		}
		size += len(text)
	}
	return size, nil
}

func glmCallBytes(value any) (int, error) {
	calls, ok := value.([]any)
	if !ok || len(calls) > 7 {
		return 0, errGLMInputShape
	}
	size := 0
	for _, value := range calls {
		call, ok := value.(map[string]any)
		if !ok || len(call) != 3 || call["type"] != "function" {
			return 0, errGLMInputShape
		}
		function, ok := call["function"].(map[string]any)
		if !ok || len(function) != 2 {
			return 0, errGLMInputShape
		}
		name, nameOK := function["name"].(string)
		arguments, argsOK := function["arguments"].(string)
		if !nameOK || !allowedTools[name] || !argsOK || unambiguousJSON([]byte(arguments)) != nil {
			return 0, errGLMInputShape
		}
		decoder := json.NewDecoder(strings.NewReader(arguments))
		decoder.UseNumber()
		var args map[string]any
		if decoder.Decode(&args) != nil || args == nil {
			return 0, errGLMInputShape
		}
		size += len("<tool_call></tool_call>") + len(name) + 2
		for key, value := range args {
			size += len("<arg_key></arg_key><arg_value></arg_value>") + len(key)
			if text, ok := value.(string); ok {
				size += len(text) // The template inserts string arguments directly.
			} else {
				length, err := glmJSONBytes(value)
				if err != nil {
					return 0, err
				}
				size += length
			}
		}
	}
	return size, nil
}

// Python/Jinja default JSON uses ", " and ": ". Go's HTML-safe string encoder
// also bounds either HF's plain tojson or Jinja's HTML-safe variant; the latter
// additionally expands apostrophes to \\u0027. Numeric schemas/arguments are
// integers in this contract: float repr expansion is deliberately unsupported.
func glmJSONBytes(value any) (int, error) {
	switch value := value.(type) {
	case nil:
		return 4, nil
	case bool:
		if value {
			return 4, nil
		}
		return 5, nil
	case string:
		encoded, _ := json.Marshal(value)
		return len(encoded) + 5*strings.Count(value, "'"), nil
	case json.Number:
		if strings.ContainsAny(string(value), ".eE") {
			return 0, errGLMInputShape
		}
		return len(value), nil
	case []any:
		size := 2
		for index, entry := range value {
			length, err := glmJSONBytes(entry)
			if err != nil {
				return 0, err
			}
			if index > 0 {
				size += 2
			}
			size += length
		}
		return size, nil
	case map[string]any:
		size, index := 2, 0
		for key, entry := range value {
			keyLength, _ := glmJSONBytes(key)
			length, err := glmJSONBytes(entry)
			if err != nil {
				return 0, err
			}
			if index > 0 {
				size += 2
			}
			size += keyLength + 2 + length
			index++
		}
		return size, nil
	default:
		return 0, errGLMInputShape
	}
}
