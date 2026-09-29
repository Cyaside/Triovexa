package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Cyaside/Triovexa/internal/coderepair"
)

func fixtureRequest(t *testing.T) []byte {
	t.Helper()
	data, err := json.Marshal(request{Operation: "run_allowed_test", RecipeID: "go-test-workload",
		Binding: coderepair.RepositoryBinding{ID: "binding-1", ServiceName: "queue-worker", Environment: "staging",
			RepositoryURL: "https://github.com/Cyaside/Triovexa", BaseRef: "main",
			AllowedPaths: []string{"internal/workload"}, TestRecipes: []string{"go-test-workload"},
			PolicyVersion: "repair-v1", Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestParseRequestAcceptsRegisteredRecipeOnly(t *testing.T) {
	data := fixtureRequest(t)
	if req, err := parseRequest(data); err != nil || req.RecipeID != "go-test-workload" {
		t.Fatalf("registered recipe rejected: %+v, %v", req, err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(map[string]any){
		"shell operation":     func(v map[string]any) { v["operation"] = "run_shell" },
		"unknown command":     func(v map[string]any) { v["command"] = "sh -c 'id'" },
		"unregistered recipe": func(v map[string]any) { v["recipe_id"] = "custom-shell" },
		"changed executable":  func(v map[string]any) { v["executable"] = "/bin/sh" },
		"url authority":       func(v map[string]any) { v["url"] = "https://example.invalid" },
	} {
		t.Run(name, func(t *testing.T) {
			copyValue := make(map[string]any, len(raw))
			for key, value := range raw {
				copyValue[key] = value
			}
			mutate(copyValue)
			encoded, err := json.Marshal(copyValue)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := parseRequest(encoded); err == nil {
				t.Fatal("sandbox accepted unregistered operation authority")
			}
		})
	}
	if _, err := parseRequest(append(data, []byte(` {"operation":"run_allowed_test"}`)...)); err == nil {
		t.Fatal("sandbox accepted trailing request")
	}
	if _, err := parseRequest([]byte(strings.Repeat("x", maxRequestBytes+1))); err == nil {
		t.Fatal("sandbox accepted oversized request")
	}
}
