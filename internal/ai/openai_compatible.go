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
	PromptTokens       int `json:"prompt_tokens"`
	CompletionTokens   int `json:"completion_tokens"`
	TotalTokens        int `json:"total_tokens"`
	CachedPromptTokens int `json:"cached_prompt_tokens,omitempty"`
	ReasoningTokens    int `json:"reasoning_tokens,omitempty"`
}

type CompletionResult struct {
	Content      string
	Provider     string
	Model        string
	Latency      time.Duration
	Usage        CompletionUsage
	UsageStatus  UsageStatus
	FinishReason string
}

type DetailedJSONCompleter interface {
	CompleteJSONDetailed(context.Context, []ChatMessage) (CompletionResult, error)
}

type OpenAICompatibleClient struct {
	mu         sync.RWMutex
	config     ProviderConfig
	client     *http.Client
	dispatcher ModelDispatcher
}

// Snapshot returns an independent client configuration for a multi-step run.
// Later UI reconfiguration cannot change its endpoint, model or credential.
func (c *OpenAICompatibleClient) Snapshot() (*OpenAICompatibleClient, error) {
	if c == nil {
		return nil, fmt.Errorf("provider is not configured")
	}
	c.mu.RLock()
	config := c.config
	dispatcher := c.dispatcher
	c.mu.RUnlock()
	frozen, err := NewOpenAICompatibleClient(config)
	if err == nil {
		frozen.SetDispatcher(dispatcher)
	}
	return frozen, err
}

// SetDispatcher installs the shared admission boundary. A dispatcher failure
// is final for this request; the client never bypasses it with a direct call.
func (c *OpenAICompatibleClient) SetDispatcher(dispatcher ModelDispatcher) {
	c.mu.Lock()
	c.dispatcher = dispatcher
	c.mu.Unlock()
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

// ConfigurationSnapshot is for trusted server-side sealing only. It is never
// returned by the connections API or supplied to the child agent.
func (c *OpenAICompatibleClient) ConfigurationSnapshot() ProviderConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	config := c.config
	config.AllowHosts = append([]string(nil), config.AllowHosts...)
	return config
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
	dispatcher := c.dispatcher
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

	var responseBody []byte
	var status int
	var latency time.Duration
	if dispatcher != nil {
		responseBody, status, latency, err = dispatcher.Dispatch(ctx, providerConfig, body)
	} else {
		request, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, completionURL(providerConfig.BaseURL), bytes.NewReader(body))
		if requestErr != nil {
			return CompletionResult{}, fmt.Errorf("create provider request: %w", requestErr)
		}
		request.Header.Set("Authorization", "Bearer "+providerConfig.APIKey)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json")
		startedAt := time.Now()
		response, callErr := httpClient.Do(request)
		err = callErr
		if response != nil {
			status = response.StatusCode
			var readErr error
			responseBody, readErr = io.ReadAll(io.LimitReader(response.Body, maxProviderResponseBytes+1))
			if err == nil {
				err = readErr
			}
			_ = response.Body.Close()
		}
		latency = time.Since(startedAt)
	}
	result := CompletionResult{Provider: providerConfig.Name, Model: providerConfig.Model, Latency: latency, UsageStatus: UsageMissing}
	if err != nil {
		return result, &CompletionCallError{Provider: providerConfig.Name, Cause: err}
	}
	if len(responseBody) > maxProviderResponseBytes {
		return result, fmt.Errorf("%s provider response exceeded byte limit", providerConfig.Name)
	}
	if status >= http.StatusMultipleChoices && status < http.StatusBadRequest {
		return result, fmt.Errorf("%s provider redirects are disabled", providerConfig.Name)
	}
	if status >= http.StatusBadRequest {
		return result, fmt.Errorf("%s provider returned status %d", providerConfig.Name, status)
	}

	var decoded struct {
		Model   string          `json:"model"`
		Choices json.RawMessage `json:"choices"`
		Usage   json.RawMessage `json:"usage"`
	}
	decoder := json.NewDecoder(bytes.NewReader(responseBody))
	if err := decoder.Decode(&decoded); err != nil {
		return result, fmt.Errorf("decode %s provider response: %w", providerConfig.Name, err)
	}
	model := strings.TrimSpace(decoded.Model)
	if model == "" {
		model = providerConfig.Model
	}
	usage, usageStatus := ParseCompletionUsage(decoded.Usage)
	result.Model, result.Usage, result.UsageStatus = model, usage, usageStatus
	var choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(decoded.Choices, &choices); err != nil {
		return result, fmt.Errorf("decode %s provider choices: %w", providerConfig.Name, err)
	}
	if len(choices) == 0 {
		return result, fmt.Errorf("%s provider response did not include choices", providerConfig.Name)
	}
	content := strings.TrimSpace(choices[0].Message.Content)
	result.Content = content
	result.FinishReason = choices[0].FinishReason
	if result.FinishReason == "length" || result.FinishReason == "content_filter" {
		return result, fmt.Errorf("%s provider completion did not finish normally", providerConfig.Name)
	}
	if content == "" {
		return result, fmt.Errorf("%s provider response content is empty", providerConfig.Name)
	}
	var jsonValue any
	if err := json.Unmarshal([]byte(content), &jsonValue); err != nil {
		return result, fmt.Errorf("%s provider returned invalid JSON content: %w", providerConfig.Name, err)
	}
	return result, nil
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
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return ProviderConfig{}, fmt.Errorf("provider base URL must not contain credentials, query parameters, or fragments")
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
