package http

import (
	"bytes"
	"context"
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Cyaside/Triovexa/internal/config"
	"github.com/Cyaside/Triovexa/internal/storage"
)

func TestReasoningConnectionConfigStoresReferenceOnly(t *testing.T) {
	repository := storage.NewMemoryStore()
	mux := stdhttp.NewServeMux()
	registerConnectionConfigurationAPI(mux, config.Config{}, repository)
	payload := `{"provider":"openai-compatible","base_url":"http://127.0.0.1:11434/v1","model":"qwen","credential_ref":"LOCAL_LLM_TOKEN","json_mode":true}`
	request := httptest.NewRequest(stdhttp.MethodPut, "/api/v1/connections/reasoning/config", strings.NewReader(payload))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != stdhttp.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	raw, err := repository.GetSetting(context.Background(), config.ReasoningConnectionSettingKey)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "secret") || !strings.Contains(raw, "LOCAL_LLM_TOKEN") {
		t.Fatalf("stored profile must contain only the reference: %s", raw)
	}
}

func TestPlaygroundFaultProxyUsesConfiguredCredential(t *testing.T) {
	supervisor := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		if r.Header.Get("Authorization") != "Bearer control-token" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["mode"] != "stall" {
			t.Fatalf("mode = %q", payload["mode"])
		}
		writeJSON(w, stdhttp.StatusOK, payload)
	}))
	defer supervisor.Close()
	mux := stdhttp.NewServeMux()
	registerPlaygroundAPI(mux, config.Config{WorkloadControlBaseURL: supervisor.URL, WorkloadControlToken: "control-token"})
	request := httptest.NewRequest(stdhttp.MethodPost, "/api/v1/playground/faults", bytes.NewBufferString(`{"mode":"stall"}`))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != stdhttp.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
}
