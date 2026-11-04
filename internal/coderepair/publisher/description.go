package publisher

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/security"
)

type publicationReport struct {
	Status      coderepair.State         `json:"status"`
	RecipeID    string                   `json:"recipe_id"`
	PatchSHA256 string                   `json:"patch_sha256"`
	BeforeExit  int                      `json:"before_exit"`
	AfterExit   int                      `json:"after_exit"`
	EvidenceIDs []string                 `json:"evidence_ids"`
	Hypothesis  string                   `json:"hypothesis"`
	Summary     coderepair.ChangeSummary `json:"summary"`
}

// Description is rendered from the accepted report and verified file list.
// The model never provides the GitHub request body or audit trailers.
type Description struct {
	Title      string
	Body       string
	CommitBody string
}

func describeChange(in Input, p coderepair.Publication, report publicationReport, files []string) (Description, error) {
	summary := coderepair.ChangeSummary{Title: security.Redact(report.Summary.Title), Changes: security.Redact(report.Summary.Changes)}
	problem := security.Redact(report.Hypothesis)
	if summary.Validate() != nil || !utf8.ValidString(problem) || len(problem) == 0 || len(problem) > 2048 ||
		strings.TrimSpace(problem) != problem || strings.IndexFunc(problem, func(r rune) bool {
		return unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
	}) >= 0 {
		return Description{}, errors.New("publication report requires an explicit problem and change summary")
	}
	var body strings.Builder
	fmt.Fprintf(&body, "## Problem\n\n%s\n\n## Changes\n\n%s\n\n", markdownText(problem), markdownText(summary.Changes))
	for _, file := range files {
		fmt.Fprintf(&body, "- %s\n", markdownText(file))
	}
	fmt.Fprintf(&body, "\n## Validation\n\n- Regression recipe: %s.\n- Before the patch: failed (exit code %d).\n- After the patch: passed (exit code %d).\n\nCI and human review are required before merge. Post-deployment recovery has not been verified.\n\n", markdownText(report.RecipeID), report.BeforeExit, report.AfterExit)
	body.WriteString("<details>\n<summary>Incident and verification details</summary>\n\n")
	fmt.Fprintf(&body, "- Incident: %s\n- Service: %s\n- Environment: %s\n- Base revision: %s\n- Patch SHA-256: %s\n- Operation: %s\n- Evidence references:\n", markdownText(in.Case.IncidentID), markdownText(in.Binding.ServiceName), markdownText(in.Binding.Environment), markdownText(in.Case.BaseSHA), markdownText(report.PatchSHA256), markdownText(p.OperationID))
	for _, id := range report.EvidenceIDs {
		fmt.Fprintf(&body, "  - %s\n", markdownText(id))
	}
	body.WriteString("\n</details>\n")
	return Description{Title: summary.Title, Body: body.String(), CommitBody: problem + "\n\n" + summary.Changes}, nil
}

// Treat generated explanations as text, never executable Markdown, HTML,
// mentions, task directives or repository links supplied by the model.
func markdownText(value string) string {
	return strings.NewReplacer("\\", "\\\\", "&", "&amp;", "<", "&lt;", ">", "&gt;", "@", "&#64;",
		"`", "\\`", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]", "#", "\\#").Replace(security.Redact(value))
}

var publicationOperation = regexp.MustCompile(`^repair-publish:[A-Za-z0-9_.-]+:[1-9][0-9]*$`)

func (d Description) valid() bool {
	return coderepair.ValidateRepairTitle(d.Title) == nil &&
		d.Body != "" && len(d.Body) <= 16*1024 && d.CommitBody != "" && len(d.CommitBody) <= 4096
}

func commitMessage(description Description, operationID, patchSHA string) string {
	return description.Title + "\n\n" + description.CommitBody + "\n\nOperation: " + operationID + "\nPatch-SHA256: " + patchSHA
}

// Historical generic messages remain readable for uncertain-write recovery.
// Identity additionally requires the approved parent, full diff and blobs.
func commitMatchesPublication(message, operationID, patchSHA string) bool {
	trailers := "\n\nOperation: " + operationID + "\nPatch-SHA256: " + patchSHA
	if message == "Repair incident (Triovexa)"+trailers {
		return true
	}
	header, _, found := strings.Cut(message, "\n\n")
	return found && strings.HasSuffix(message, trailers) && strings.Count(message, "\nOperation: ") == 1 &&
		strings.Count(message, "\nPatch-SHA256: ") == 1 &&
		coderepair.ValidateRepairTitle(header) == nil
}
