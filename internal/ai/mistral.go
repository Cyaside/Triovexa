package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type JSONCompleter interface {
	CompleteJSON(context.Context, []ChatMessage) (string, error)
}

type MistralClient struct {
	apiKey  string
	model   string
	baseURL string
	client  *http.Client
}

func NewMistralClient(apiKey string, model string) *MistralClient {
	return &MistralClient{
		apiKey:  strings.TrimSpace(apiKey),
		model:   strings.TrimSpace(model),
		baseURL: "https://api.mistral.ai",
		client: &http.Client{
			Timeout: 20 * time.Second,
		},
	}
}

func (c *MistralClient) Configured() bool {
	return c.apiKey != "" && c.model != ""
}

func (c *MistralClient) CompleteJSON(ctx context.Context, messages []ChatMessage) (string, error) {
	if !c.Configured() {
		return "", fmt.Errorf("mistral client is not configured")
	}

	requestBody := map[string]any{
		"model":    messagesModel(c.model),
		"messages": messages,
		"response_format": map[string]any{
			"type": "json_object",
		},
	}

	body, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("marshal mistral request: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create mistral request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+c.apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")

	response, err := c.client.Do(request)
	if err != nil {
		return "", fmt.Errorf("call mistral chat completion: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode >= http.StatusBadRequest {
		return "", fmt.Errorf("mistral chat completion returned %d", response.StatusCode)
	}

	var payload struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode mistral response: %w", err)
	}
	if len(payload.Choices) == 0 {
		return "", fmt.Errorf("mistral response did not include any choices")
	}

	content := strings.TrimSpace(payload.Choices[0].Message.Content)
	if content == "" {
		return "", fmt.Errorf("mistral response content is empty")
	}

	return content, nil
}

func messagesModel(value string) string {
	if strings.TrimSpace(value) == "" {
		return "mistral-small-latest"
	}
	return value
}
