package security

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestRedactRemovesCredentialsFromErrorsAndURLs(t *testing.T) {
	input := `Authorization: Bearer top.secret https://admin:password@example.test/v1?api_key=sk-live&token=abc {"secret":"hidden"}`
	output := Redact(input)
	for _, secret := range []string{"top.secret", "admin:password", "sk-live", "token=abc", `"hidden"`} {
		if strings.Contains(output, secret) {
			t.Fatalf("redacted output still contains %q: %s", secret, output)
		}
	}
}

func TestRedactingHandlerSanitizesStructuredLogAttributes(t *testing.T) {
	var buffer bytes.Buffer
	handler := NewRedactingHandler(slog.NewJSONHandler(&buffer, nil))
	record := slog.NewRecord(time.Now(), slog.LevelError, "provider failed", 0)
	record.AddAttrs(slog.String("error", "request failed: https://user:pass@example.test?token=secret-value"))
	if err := handler.Handle(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	output := buffer.String()
	if strings.Contains(output, "user:pass") || strings.Contains(output, "secret-value") {
		t.Fatalf("log was not redacted: %s", output)
	}
}
