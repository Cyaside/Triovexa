package auth

import (
	"context"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/storage"
)

func TestLoginAndCSRF(t *testing.T) {
	service := NewService(storage.NewMemoryStore(), time.Hour)
	if _, err := service.CreateUser(context.Background(), "operator", "a-long-test-password", domain.RoleOperator); err != nil {
		t.Fatal(err)
	}
	grant, err := service.Login(context.Background(), "operator", "a-long-test-password")
	if err != nil {
		t.Fatal(err)
	}
	user, session, err := service.Authenticate(context.Background(), grant.Token)
	if err != nil || user.Role != domain.RoleOperator {
		t.Fatalf("Authenticate() user=%#v err=%v", user, err)
	}
	if !service.ValidateCSRF(session, grant.CSRFToken) || service.ValidateCSRF(session, "wrong") {
		t.Fatal("csrf validation mismatch")
	}
}
