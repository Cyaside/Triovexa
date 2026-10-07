package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/config"
	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/storage/postgres"
)

type onboardingStore struct {
	*postgres.PostgresStore
	binding coderepair.RepositoryBinding
}

func (s *onboardingStore) CreateRepositoryBinding(_ context.Context, b coderepair.RepositoryBinding) error {
	s.binding = b
	return nil
}

func TestRepositoryOnboardingRequiresAdminAndPinsGrantActor(t *testing.T) {
	store := &onboardingStore{}
	mux := http.NewServeMux()
	registerRepairAPI(mux, config.Config{}, store, nil, nil)
	profile := &coderepair.ValidationProfile{ID: "python-tests", Version: "python-v1", Image: "triovexa-repair-sandbox:python", RootFiles: []string{"pyproject.toml"}, ProtectedPaths: []string{"tests"}, Test: coderepair.ValidationCommand{Executable: "/usr/local/bin/python", Arguments: []string{"-m", "unittest"}, TimeoutSeconds: 60}, ExpectedTestName: "test_parser", ExpectedFailure: "parser failed"}
	for _, role := range []string{domain.RoleOperator, domain.RoleAdmin} {
		body, _ := json.Marshal(map[string]any{"service_name": "worker", "environment": "staging", "repository_url": "https://github.com/owner/worker", "base_ref": "main", "allowed_paths": []string{"src"}, "test_recipes": []string{"python-tests"}, "validation_profile": profile, "credential_ref": "env:REPAIR_GITHUB_READ_TOKEN", "automation": coderepair.AutomationPolicy{Enabled: true, AuthorizedBy: "spoofed-admin", ExpiresAt: time.Now().Add(time.Hour), CampaignID: "test-campaign", MaxInvestigations: 1, MaxModelRequests: 2}})
		r := httptest.NewRequest(http.MethodPost, "/api/v1/repair/bindings", bytes.NewReader(body))
		r = r.WithContext(context.WithValue(r.Context(), identityContextKey{}, requestIdentity{User: domain.User{ID: "real-admin", Role: role}}))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if role != domain.RoleAdmin {
			if w.Code != http.StatusForbidden || store.binding.ID != "" {
				t.Fatalf("operator granted automation: %d", w.Code)
			}
			continue
		}
		if w.Code != http.StatusCreated || store.binding.Automation.AuthorizedBy != "real-admin" || store.binding.ValidationProfile.Test.Executable != "/usr/local/bin/python" {
			t.Fatalf("onboarding: %d %s", w.Code, w.Body.String())
		}
		if store.binding.CredentialRef != "env:REPAIR_GITHUB_READ_TOKEN" {
			t.Fatal("credential reference was not persisted")
		}
	}
}
