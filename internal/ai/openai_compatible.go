package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxProviderResponseBytes = 2 << 20

type ProviderConfig struct {
	Name       string
	BaseURL    string
	APIKey     string
	Model      string
	JSONMode   bool
	Timeout    time.Duration
	AllowHTTP  bool
	AllowHosts []string
}

type CompletionUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type CompletionResult struct {
	Content  string
	Provider string
	Model    string
	Latency  time.Duration
	Usage    CompletionUsage
}

type DetailedJSONCompleter interface {
	CompleteJSONDetailed(context.Context, []ChatMessage) (CompletionResult, error)
}

type OpenAICompatibleClient struct {
	config ProviderConfig
	client *http.Client
}

func NewOpenAICompatibleClient(config ProviderConfig) (*OpenAICompatibleClient, error) {
	normalized, err := normalizeProviderConfig(config)
	if err != nil {
		return nil, err
	}
	return &OpenAICompatibleClient{
		config: normalized,
		client: &http.Client{
			Timeout: normalized.Timeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (c *OpenAICompatibleClient) Configured() bool {
	return c != nil && c.config.BaseURL != "" && c.config.Model != "" && c.config.APIKey != ""
}

func (c *OpenAICompatibleClient) Provider() string {
	if c == nil {
		return ""
	}
	return c.config.Name
}

func (c *OpenAICompatibleClient) Model() string {
	if c == nil {
		return ""
	}
	return c.config.Model
}

func (c *OpenAICompatibleClient) CompleteJSON(ctx context.Context, messages []ChatMessage) (string, error) {
	result, err := c.CompleteJSONDetailed(ctx, messages)
	return result.Content, err
}

func (c *OpenAICompatibleClient) CompleteJSONDetailed(ctx context.Context, messages []ChatMessage) (CompletionResult, error) {
	if !c.Configured() {
		return CompletionResult{}, fmt.Errorf("%s provider is not configured", c.Provider())
	}

	payload := map[string]any{
		"model":    c.config.Model,
		"messages": messages,
	}
	if c.config.JSONMode {
		payload["response_format"] = map[string]any{"type": "json_object"}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return CompletionResult{}, fmt.Errorf("marshal provider request: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, completionURL(c.config.BaseURL), bytes.NewReader(body))
	if err != nil {
		return CompletionResult{}, fmt.Errorf("create provider request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")

	startedAt := time.Now()
	response, err := c.client.Do(request)
	latency := time.Since(startedAt)
	if err != nil {
		return CompletionResult{}, fmt.Errorf("call %s provider: %w", c.Provider(), err)
	}
	defer response.Body.Close()
	if response.StatusCode >= http.StatusMultipleChoices && response.StatusCode < http.StatusBadRequest {
		return CompletionResult{}, fmt.Errorf("%s provider redirects are disabled", c.Provider())
	}
	if response.StatusCode >= http.StatusBadRequest {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return CompletionResult{}, fmt.Errorf("%s provider returned status %d", c.Provider(), response.StatusCode)
	}

	var decoded struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage CompletionUsage `json:"usage"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxProviderResponseBytes))
	if err := decoder.Decode(&decoded); err != nil {
		return CompletionResult{}, fmt.Errorf("decode %s provider response: %w", c.Provider(), err)
	}
	if len(decoded.Choices) == 0 {
		return CompletionResult{}, fmt.Errorf("%s provider response did not include choices", c.Provider())
	}
	content := strings.TrimSpace(decoded.Choices[0].Message.Content)
	if content == "" {
		return CompletionResult{}, fmt.Errorf("%s provider response content is empty", c.Provider())
	}
	var jsonValue any
	if err := json.Unmarshal([]byte(content), &jsonValue); err != nil {
		return CompletionResult{}, fmt.Errorf("%s provider returned invalid JSON content: %w", c.Provider(), err)
	}
	model := strings.TrimSpace(decoded.Model)
	if model == "" {
		model = c.config.Model
	}
	return CompletionResult{Content: content, Provider: c.Provider(), Model: model, Latency: latency, Usage: decoded.Usage}, nil
}

func normalizeProviderConfig(config ProviderConfig) (ProviderConfig, error) {
	config.Name = strings.ToLower(strings.TrimSpace(config.Name))
	if config.Name == "" {
		config.Name = "openai-compatible"
	}
	config.BaseURL = strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	config.APIKey = strings.TrimSpace(config.APIKey)
	config.Model = strings.TrimSpace(config.Model)
	if config.Timeout <= 0 {
		config.Timeout = 20 * time.Second
	}
	if config.BaseURL == "" {
		return config, nil
	}
	parsed, err := url.Parse(config.BaseURL)
	if err != nil || parsed.Hostname() == "" {
		return ProviderConfig{}, fmt.Errorf("invalid provider base URL")
	}
	if parsed.Scheme != "https" {
		if parsed.Scheme != "http" || !config.AllowHTTP || !isLoopbackHost(parsed.Hostname()) {
			return ProviderConfig{}, fmt.Errorf("provider base URL must use HTTPS; HTTP is only allowed for loopback local mode")
		}
	}
	if len(config.AllowHosts) > 0 && !containsFold(config.AllowHosts, parsed.Hostname()) {
		return ProviderConfig{}, fmt.Errorf("provider host %q is not allowlisted", parsed.Hostname())
	}
	return config, nil
}

func completionURL(baseURL string) string {
	baseURL = strings.TrimRight(baseURL, "/")
	if strings.HasSuffix(strings.ToLower(baseURL), "/v1") {
		return baseURL + "/chat/completions"
	}
	return baseURL + "/v1/chat/completions"
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(strings.ToLower(host))
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func containsFold(values []string, value string) bool {
	for _, candidate := range values {
		if strings.EqualFold(strings.TrimSpace(candidate), value) {
			return true
		}
	}
	return false
}
