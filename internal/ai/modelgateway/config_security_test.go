package modelgateway

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cyaside/Triovexa/internal/ai/admission"
)

func TestBudgetConfigurationRejectsAmbiguousAndOversizedFiles(t *testing.T) {
	config := BudgetConfig{Campaign: admission.Campaign{ID: "offline", Profile: "offline-fixture", Offline: true,
		MaxInputTokens: 12000, MaxRequests: 6}, ConfigVersion: "v1", MaxInputTokens: 6000, MaxOutputTokens: 1500}
	raw, _ := json.Marshal(config)
	for _, scenario := range []struct {
		name, value string
		valid       bool
	}{
		{"valid", string(raw), true},
		{"duplicate limit", strings.Replace(string(raw), `"max_output_tokens":1500`, `"max_output_tokens":1500,"max_output_tokens":100000`, 1), false},
		{"unknown field", string(raw[:len(raw)-1]) + `,"unlimited":true}`, false},
		{"trailing payload", string(raw) + ` {}`, false},
		{"suffix outside read limit", string(raw) + strings.Repeat(" ", 16*1024) + `{}`, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "admission.json")
			if err := os.WriteFile(path, []byte(scenario.value), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadBudgetConfig(path)
			if (err == nil) != scenario.valid {
				t.Fatalf("valid=%v, err=%v", scenario.valid, err)
			}
		})
	}
}
