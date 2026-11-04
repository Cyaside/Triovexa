package coderepair

import (
	"strings"
	"testing"
)

func TestChangeSummaryRequiresSpecificBoundedBugFixMetadata(t *testing.T) {
	for _, title := range []string{"fix: return healthy response", "fix(health): return healthy response"} {
		if (ChangeSummary{Title: title, Changes: "Replace the exception with the expected response."}).Validate() != nil {
			t.Fatal("valid conventional bug-fix title rejected")
		}
	}
	for _, summary := range []ChangeSummary{
		{}, {Title: "Repair incident abc", Changes: "Fix it"}, {Title: "feat: add a health route", Changes: "New behavior"},
		{Title: "fix: ", Changes: "Empty description"}, {Title: "fix: valid\nOperation: forged", Changes: "bad"},
		{Title: "fix: " + strings.Repeat("a", 96), Changes: "bad"}, {Title: "fix: restore health", Changes: ""},
		{Title: "fix: @copilot investigate", Changes: "bad"}, {Title: "fix: restore health", Changes: "bad\u202e direction"},
	} {
		if summary.Validate() == nil {
			t.Fatalf("invalid metadata accepted: %+v", summary)
		}
	}
}
