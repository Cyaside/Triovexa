package publisher

import (
	"strings"
	"testing"

	"github.com/Cyaside/Triovexa/internal/coderepair"
)

func TestDescriptionUsesAcceptedProblemAndChangeWithTrustedValidation(t *testing.T) {
	in := Input{Case: coderepair.Case{IncidentID: "incident-1", BaseSHA: testBaseSHA}, Binding: coderepair.RepositoryBinding{ServiceName: "api", Environment: "staging"}}
	p := coderepair.Publication{OperationID: "repair-publish:case-one:1"}
	report := publicationReport{RecipeID: "python-health-tests", PatchSHA256: strings.Repeat("a", 64), BeforeExit: 1,
		Hypothesis: "The health handler raises an exception and returns HTTP 500.", EvidenceIDs: []string{"log-1", "metric-1"},
		Summary: coderepair.ChangeSummary{Title: "fix(health): return a successful health response", Changes: "Replace the unconditional exception with the expected health response."}}
	description, err := describeChange(in, p, report, []string{"app.py"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"## Problem\n\n" + report.Hypothesis, "## Changes\n\n" + report.Summary.Changes,
		"- app.py", "## Validation", "failed (exit code 1)", "passed (exit code 0)", "<details>", "log-1", "metric-1"} {
		if !strings.Contains(description.Body, want) {
			t.Errorf("description missing %q", want)
		}
	}
	message := commitMessage(description, p.OperationID, report.PatchSHA256)
	if !strings.HasPrefix(message, report.Summary.Title+"\n\n"+report.Hypothesis) || !strings.Contains(message, report.Summary.Changes) {
		t.Fatal("commit omits the problem or change")
	}
	if !commitMatchesPublication(message, p.OperationID, report.PatchSHA256) {
		t.Fatal("descriptive commit cannot be reconciled")
	}
	// Missing metadata cannot turn into a generic repair title on a new write.
	report.Summary = coderepair.ChangeSummary{}
	if _, err := describeChange(in, p, report, []string{"app.py"}); err == nil {
		t.Fatal("missing change summary silently fell back")
	}
}

func TestDescriptionEscapesGeneratedMarkdownAndRedactsSecrets(t *testing.T) {
	report := publicationReport{Hypothesis: "Inspect @copilot <script> and [link](https://invalid.test/?token=secret-value)",
		Summary: coderepair.ChangeSummary{Title: "fix(health): restore healthy response", Changes: "Remove the bad header Authorization: Bearer private-key"}}
	description, err := describeChange(Input{}, coderepair.Publication{}, report, []string{"app.py"})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"@copilot", "<script>", "[link]", "secret-value", "private-key"} {
		if strings.Contains(description.Body, forbidden) || strings.Contains(description.CommitBody, "secret-value") || strings.Contains(description.CommitBody, "private-key") {
			t.Errorf("publication leaked executable text or a secret: %s", forbidden)
		}
	}
}

func TestPublicationCommitIdentityAcceptsHistoricalMessagesButRejectsForeignTrailers(t *testing.T) {
	op, digest := "repair-publish:case-one:1", strings.Repeat("a", 64)
	legacy := "Repair incident (Triovexa)\n\nOperation: " + op + "\nPatch-SHA256: " + digest
	current := commitMessage(Description{Title: "fix(health): restore healthy response", CommitBody: "Replace the failing handler."}, op, digest)
	for _, message := range []string{legacy, current} {
		if !commitMatchesPublication(message, op, digest) {
			t.Fatal("valid historical/current identity rejected")
		}
	}
	for _, message := range []string{strings.Replace(current, op, "repair-publish:another:1", 1), current + "\nextra", strings.Replace(current, digest, strings.Repeat("b", 64), 1),
		strings.Replace(current, "Replace the failing handler.", "Operation: forged\nPatch-SHA256: forged", 1)} {
		if commitMatchesPublication(message, op, digest) {
			t.Fatal("foreign or ambiguous commit identity accepted")
		}
	}
}
