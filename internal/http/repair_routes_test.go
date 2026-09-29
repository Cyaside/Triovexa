package http

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/config"
	"github.com/Cyaside/Triovexa/internal/storage"
	"github.com/Cyaside/Triovexa/internal/storage/memory"
	"github.com/Cyaside/Triovexa/internal/storage/postgres"
	"github.com/Cyaside/Triovexa/internal/telemetry"
)

var _ repairAPIStore = (*postgres.PostgresStore)(nil)

type recordingRepairWebhookStore struct {
	storage.Repository
	events []coderepair.PREvent
}

func (s *recordingRepairWebhookStore) ApplyRepairPREvent(_ context.Context, event coderepair.PREvent) (bool, error) {
	s.events = append(s.events, event)
	return true, nil
}

func TestRepairGitHubWebhookRequiresSignatureAndCarriesIdentity(t *testing.T) {
	store := &recordingRepairWebhookStore{Repository: memory.NewMemoryStore()}
	cfg := config.Config{DeploymentMode: "internal", RepairGitHubWebhookSecret: "webhook-secret",
		WebhookRateLimit: 10, WebhookRateWindow: time.Minute}
	mux := http.NewServeMux()
	registerRepairRoutes(mux, cfg, store, nil, nil)
	handler := securityMiddleware(cfg, nil, telemetry.NewRecorder(), mux)
	payload := []byte(`{"action":"closed","repository":{"full_name":"acme/worker"},"pull_request":{"number":4,"merged":true,"merge_commit_sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","head":{"ref":"triovexa/repair/case/1","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"base":{"ref":"main"}}}`)
	request := func(signature string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/webhooks/github/repair", bytes.NewReader(payload))
		req.Header.Set("X-GitHub-Event", "pull_request")
		req.Header.Set("X-GitHub-Delivery", uuid.NewString())
		req.Header.Set("X-Hub-Signature-256", signature)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	if response := request("sha256=deadbeef"); response.Code != http.StatusUnauthorized || len(store.events) != 0 {
		t.Fatalf("unsigned delivery accepted: status=%d events=%d", response.Code, len(store.events))
	}
	mac := hmac.New(sha256.New, []byte(cfg.RepairGitHubWebhookSecret))
	mac.Write(payload)
	if response := request("sha256=" + hex.EncodeToString(mac.Sum(nil))); response.Code != http.StatusOK || len(store.events) != 1 {
		t.Fatalf("signed delivery rejected: status=%d events=%d body=%s", response.Code, len(store.events), response.Body.String())
	}
	if event := store.events[0]; event.Repository != "acme/worker" || event.Number != 4 || !event.Merged || event.Branch != "triovexa/repair/case/1" {
		t.Fatalf("webhook identity was lost: %+v", event)
	}
}
