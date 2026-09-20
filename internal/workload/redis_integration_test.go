package workload

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func TestRedisStreamWorkerProcessesAndDeduplicatesIntegration(t *testing.T) {
	address := os.Getenv("TEST_REDIS_ADDRESS")
	if address == "" {
		t.Skip("TEST_REDIS_ADDRESS is not configured")
	}
	client := RedisClient(address)
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	jobID := "integration-" + uuid.NewString()
	defer client.Del(context.Background(), "triovexa:processed:"+jobID)
	workerCtx, stopWorker := context.WithCancel(context.Background())
	defer stopWorker()
	workerDone := make(chan error, 1)
	go func() { workerDone <- RunWorker(workerCtx, client, slog.New(slog.NewTextHandler(io.Discard, nil))) }()
	if err := client.XAdd(ctx, &redis.XAddArgs{Stream: stream, Values: map[string]any{"job_id": jobID, "created_at": time.Now().UTC().Format(time.RFC3339Nano)}}).Err(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if value, _ := client.Get(ctx, "triovexa:processed:"+jobID).Result(); value == "1" {
			stopWorker()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("worker did not process the Redis stream job")
}

func TestSupervisorAcceptsSnakeCaseOperationContractIntegration(t *testing.T) {
	address := os.Getenv("TEST_REDIS_ADDRESS")
	if address == "" {
		t.Skip("TEST_REDIS_ADDRESS is not configured")
	}
	client := RedisClient(address)
	defer client.Close()
	operationID := "integration-" + uuid.NewString()
	defer client.Del(context.Background(), "triovexa:operation:"+operationID, "triovexa:consumer:paused")

	supervisor := NewSupervisor(client, "test-token", slog.New(slog.NewTextHandler(io.Discard, nil)))
	server := httptest.NewServer(supervisor.Handler(context.Background()))
	defer server.Close()
	payload, _ := json.Marshal(map[string]any{"operation_id": operationID, "operation": "pause_consumer", "target": "queue-worker", "requested_by": "integration-test"})
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/operations", bytes.NewReader(payload))
	request.Header.Set("Authorization", "Bearer test-token")
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("operation status = %d body=%s", response.StatusCode, body)
	}
	var result map[string]any
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result["operation_id"] != operationID || result["status"] != "succeeded" {
		t.Fatalf("operation result = %#v", result)
	}
}
