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
	"sync"
	"time"
)

const maxProviderResponseBytes = 2 << 20

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type JSONCompleter interface {
	CompleteJSON(context.Context, []ChatMessage) (string, error)
}

type ProviderConfig struct {
	Name             string
	BaseURL          string
	APIKey           string
	Model            string
	JSONMode         bool
	Timeout          time.Duration
	AllowHTTP        bool
	AllowHosts       []string
	RequireAllowlist bool
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
	mu     sync.RWMutex
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
	if c == nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return providerConfigured(c.config)
}

func (c *OpenAICompatibleClient) Provider() string {
	if c == nil {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.config.Name
}

func (c *OpenAICompatibleClient) Model() string {
	if c == nil {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.config.Model
}

// Reconfigure atomically replaces the provider used by future completions.
// Callers can safely reconfigure while an existing request is in flight; that
// request finishes with the client snapshot it started with.
func (c *OpenAICompatibleClient) Reconfigure(config ProviderConfig) error {
	if strings.TrimSpace(config.APIKey) == "" {
		c.mu.RLock()
		config.APIKey = c.config.APIKey
		c.mu.RUnlock()
	}
	normalized, err := normalizeProviderConfig(config)
	if err != nil {
		return err
	}
	client := &http.Client{
		Timeout: normalized.Timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	c.mu.Lock()
	c.config = normalized
	c.client = client
	c.mu.Unlock()
	return nil
}

func (c *OpenAICompatibleClient) CompleteJSON(ctx context.Context, messages []ChatMessage) (string, error) {
	result, err := c.CompleteJSONDetailed(ctx, messages)
	return result.Content, err
}

func (c *OpenAICompatibleClient) CompleteJSONDetailed(ctx context.Context, messages []ChatMessage) (CompletionResult, error) {
	if c == nil {
		return CompletionResult{}, fmt.Errorf("provider is not configured")
	}
	c.mu.RLock()
	providerConfig := c.config
	httpClient := c.client
	c.mu.RUnlock()
	if !providerConfigured(providerConfig) {
		return CompletionResult{}, fmt.Errorf("%s provider is not configured", providerConfig.Name)
	}

	payload := map[string]any{
		"model":    providerConfig.Model,
		"messages": messages,
	}
	if providerConfig.JSONMode {
		payload["response_format"] = map[string]any{"type": "json_object"}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return CompletionResult{}, fmt.Errorf("marshal provider request: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, completionURL(providerConfig.BaseURL), bytes.NewReader(body))
	if err != nil {
		return CompletionResult{}, fmt.Errorf("create provider request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+providerConfig.APIKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")

	startedAt := time.Now()
	response, err := httpClient.Do(request)
	latency := time.Since(startedAt)
	if err != nil {
		return CompletionResult{}, fmt.Errorf("call %s provider: %w", providerConfig.Name, err)
	}
	defer response.Body.Close()
	if response.StatusCode >= http.StatusMultipleChoices && response.StatusCode < http.StatusBadRequest {
		return CompletionResult{}, fmt.Errorf("%s provider redirects are disabled", providerConfig.Name)
	}
	if response.StatusCode >= http.StatusBadRequest {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return CompletionResult{}, fmt.Errorf("%s provider returned status %d", providerConfig.Name, response.StatusCode)
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
		return CompletionResult{}, fmt.Errorf("decode %s provider response: %w", providerConfig.Name, err)
	}
	if len(decoded.Choices) == 0 {
		return CompletionResult{}, fmt.Errorf("%s provider response did not include choices", providerConfig.Name)
	}
	content := strings.TrimSpace(decoded.Choices[0].Message.Content)
	if content == "" {
		return CompletionResult{}, fmt.Errorf("%s provider response content is empty", providerConfig.Name)
	}
	var jsonValue any
	if err := json.Unmarshal([]byte(content), &jsonValue); err != nil {
		return CompletionResult{}, fmt.Errorf("%s provider returned invalid JSON content: %w", providerConfig.Name, err)
	}
	model := strings.TrimSpace(decoded.Model)
	if model == "" {
		model = providerConfig.Model
	}
	return CompletionResult{Content: content, Provider: providerConfig.Name, Model: model, Latency: latency, Usage: decoded.Usage}, nil
}

func providerConfigured(config ProviderConfig) bool {
	return config.BaseURL != "" && config.Model != "" && config.APIKey != ""
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
	if config.RequireAllowlist && len(config.AllowHosts) == 0 {
		return ProviderConfig{}, fmt.Errorf("provider host allowlist is required")
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
