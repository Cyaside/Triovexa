package execution

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

	"github.com/Cyaside/Triovexa/internal/domain"
)

// ControlAdapter calls the bounded workload supervisor API. It never accepts a
// URL or command from an incident payload.
type ControlAdapter struct {
	baseURL string
	token   string
	client  *http.Client
}

func NewControlAdapter(baseURL, token string) *ControlAdapter {
	return &ControlAdapter{baseURL: strings.TrimRight(baseURL, "/"), token: strings.TrimSpace(token), client: &http.Client{Timeout: 10 * time.Second}}
}

func (a *ControlAdapter) Execute(ctx context.Context, action domain.CandidateAction, request AdapterRequest) (AdapterResult, error) {
	operation, err := controlOperation(action.ActionType)
	if err != nil {
		return AdapterResult{ExecutorType: "workload-control-api"}, err
	}
	payload := map[string]any{"operation_id": request.IdempotencyKey, "operation": operation, "target": "queue-worker", "requested_by": request.InitiatedBy}
	body, _ := json.Marshal(payload)
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/operations", bytes.NewReader(body))
	if err != nil {
		return AdapterResult{ExecutorType: "workload-control-api"}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Authorization", "Bearer "+a.token)
	response, err := a.client.Do(httpRequest)
	if err != nil {
		return AdapterResult{ExecutorType: "workload-control-api"}, RetryableError{Err: err}
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return AdapterResult{ExecutorType: "workload-control-api"}, err
	}
	var result map[string]any
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return AdapterResult{ExecutorType: "workload-control-api"}, fmt.Errorf("decode control response: %w", err)
	}
	if response.StatusCode >= 500 {
		return AdapterResult{ExecutorType: "workload-control-api"}, RetryableError{Err: fmt.Errorf("control API returned %d", response.StatusCode)}
	}
	if response.StatusCode >= 400 {
		return AdapterResult{ExecutorType: "workload-control-api"}, fmt.Errorf("control API returned %d", response.StatusCode)
	}
	return AdapterResult{ExecutorType: "workload-control-api", Payload: result}, nil
}

func (a *ControlAdapter) Reconcile(ctx context.Context, _ domain.CandidateAction, request AdapterRequest) (AdapterResult, ReconciliationStatus, error) {
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, a.baseURL+"/operations/"+url.PathEscape(request.IdempotencyKey), nil)
	if err != nil {
		return AdapterResult{ExecutorType: "workload-control-api"}, ReconciliationUnknown, err
	}
	httpRequest.Header.Set("Authorization", "Bearer "+a.token)
	response, err := a.client.Do(httpRequest)
	if err != nil {
		return AdapterResult{ExecutorType: "workload-control-api"}, ReconciliationUnknown, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return AdapterResult{ExecutorType: "workload-control-api", Payload: map[string]any{"operation_id": request.IdempotencyKey}}, ReconciliationUnknown, nil
	}
	if response.StatusCode >= http.StatusBadRequest {
		return AdapterResult{ExecutorType: "workload-control-api"}, ReconciliationUnknown, fmt.Errorf("control operation status returned %d", response.StatusCode)
	}
	var payload map[string]any
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return AdapterResult{ExecutorType: "workload-control-api"}, ReconciliationUnknown, fmt.Errorf("decode control operation status: %w", err)
	}
	status, _ := payload["status"].(string)
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "succeeded":
		return AdapterResult{ExecutorType: "workload-control-api", Payload: payload}, ReconciliationSucceeded, nil
	case "failed":
		return AdapterResult{ExecutorType: "workload-control-api", Payload: payload}, ReconciliationFailed, nil
	case "pending", "running", "started":
		return AdapterResult{ExecutorType: "workload-control-api", Payload: payload}, ReconciliationPending, nil
	default:
		return AdapterResult{ExecutorType: "workload-control-api", Payload: payload}, ReconciliationUnknown, nil
	}
}

func controlOperation(actionType string) (string, error) {
	switch actionType {
	case "restart_demo_worker", "restart_worker":
		return "restart_worker", nil
	case "pause_demo_queue_consumer", "pause_consumer":
		return "pause_consumer", nil
	case "resume_demo_queue_consumer", "resume_consumer":
		return "resume_consumer", nil
	default:
		return "", fmt.Errorf("action %q is not supported by workload control", actionType)
	}
}
