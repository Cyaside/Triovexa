package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Cyaside/Triovexa/internal/domain"
)

type DemoAdapter struct {
	baseURL string
	client  *http.Client
}

func NewDemoAdapter(baseURL string) *DemoAdapter {
	return &DemoAdapter{
		baseURL: strings.TrimRight(baseURL, "/"),
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (a *DemoAdapter) Execute(ctx context.Context, action domain.CandidateAction, request AdapterRequest) (AdapterResult, error) {
	endpoint, payload, err := demoEndpointForAction(action)
	if err != nil {
		return AdapterResult{ExecutorType: "demo-http-adapter"}, err
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return AdapterResult{ExecutorType: "demo-http-adapter"}, fmt.Errorf("marshal adapter payload: %w", err)
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+endpoint, bytes.NewReader(body))
	if err != nil {
		return AdapterResult{ExecutorType: "demo-http-adapter"}, fmt.Errorf("create demo action request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Idempotency-Key", request.IdempotencyKey)
	httpRequest.Header.Set("X-Triggered-By", request.InitiatedBy)

	response, err := a.client.Do(httpRequest)
	if err != nil {
		return AdapterResult{ExecutorType: "demo-http-adapter"}, err
	}
	defer response.Body.Close()

	var payloadResponse map[string]any
	if err := json.NewDecoder(response.Body).Decode(&payloadResponse); err != nil {
		return AdapterResult{ExecutorType: "demo-http-adapter"}, fmt.Errorf("decode demo action response: %w", err)
	}

	if response.StatusCode >= http.StatusInternalServerError {
		return AdapterResult{ExecutorType: "demo-http-adapter"}, RetryableError{Err: fmt.Errorf("demo action returned %d", response.StatusCode)}
	}
	if response.StatusCode >= http.StatusBadRequest {
		return AdapterResult{ExecutorType: "demo-http-adapter"}, fmt.Errorf("demo action returned %d", response.StatusCode)
	}

	return AdapterResult{
		ExecutorType: "demo-http-adapter",
		Payload:      payloadResponse,
	}, nil
}

func demoEndpointForAction(action domain.CandidateAction) (string, map[string]any, error) {
	var parameters map[string]any
	if strings.TrimSpace(action.ParametersJSON) != "" {
		if err := json.Unmarshal([]byte(action.ParametersJSON), &parameters); err != nil {
			return "", nil, fmt.Errorf("invalid candidate action parameters: %w", err)
		}
	} else {
		parameters = map[string]any{}
	}

	switch action.ActionType {
	case "restart_demo_worker":
		return "/actions/restart-worker", parameters, nil
	case "retry_demo_background_job":
		return "/actions/retry-job", parameters, nil
	case "refresh_demo_cache":
		return "/actions/refresh-cache", parameters, nil
	case "pause_demo_queue_consumer":
		return "/actions/pause-queue-consumer", parameters, nil
	case "resume_demo_queue_consumer":
		return "/actions/resume-queue-consumer", parameters, nil
	default:
		return "", nil, fmt.Errorf("unsupported demo action %q", action.ActionType)
	}
}
