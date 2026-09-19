package ai

import (
	"context"
	"strings"
)

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type JSONCompleter interface {
	CompleteJSON(context.Context, []ChatMessage) (string, error)
}

type MistralClient struct {
	apiKey   string
	model    string
	baseURL  string
	delegate *OpenAICompatibleClient
}

func NewMistralClient(apiKey string, model string) *MistralClient {
	return &MistralClient{
		apiKey:  strings.TrimSpace(apiKey),
		model:   strings.TrimSpace(model),
		baseURL: "https://api.mistral.ai",
	}
}

func (c *MistralClient) Configured() bool {
	return c.apiKey != "" && c.model != ""
}

func (c *MistralClient) CompleteJSON(ctx context.Context, messages []ChatMessage) (string, error) {
	client, err := NewOpenAICompatibleClient(ProviderConfig{
		Name: "mistral", BaseURL: c.baseURL, APIKey: c.apiKey, Model: messagesModel(c.model), JSONMode: true,
		AllowHTTP: isLoopbackHostFromURL(c.baseURL),
	})
	if err != nil {
		return "", err
	}
	c.delegate = client
	return client.CompleteJSON(ctx, messages)
}

func messagesModel(value string) string {
	if strings.TrimSpace(value) == "" {
		return "mistral-small-latest"
	}
	return value
}

func isLoopbackHostFromURL(raw string) bool {
	for _, prefix := range []string{"http://localhost", "http://127.0.0.1", "http://[::1]"} {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(raw)), prefix) {
			return true
		}
	}
	return false
}
