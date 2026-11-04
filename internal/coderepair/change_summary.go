package coderepair

import (
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ChangeSummary describes the accepted candidate, not an instruction to GitHub.
// Incident repair is a bug fix; arbitrary commit types and release directives
// are deliberately outside this workflow.
type ChangeSummary struct {
	Title   string `json:"title"`
	Changes string `json:"changes"`
}

var repairTitle = regexp.MustCompile(`^fix(\([a-z0-9][a-z0-9._/-]{0,39}\))?: \S.*$`)

func (s ChangeSummary) Validate() error {
	if ValidateRepairTitle(s.Title) != nil || !publicationText(s.Changes, 1024) {
		return errors.New("repair summary requires a concise fix(scope): title and an explanation of the actual change")
	}
	return nil
}

func ValidateRepairTitle(title string) error {
	if !publicationText(title, 96) || !repairTitle.MatchString(title) || strings.ContainsAny(title, "`<>@[]") {
		return errors.New("repair title must describe the bug fix using fix(scope): description")
	}
	return nil
}

func publicationText(value string, maxBytes int) bool {
	return value != "" && len(value) <= maxBytes && utf8.ValidString(value) &&
		strings.TrimSpace(value) == value && strings.IndexFunc(value, func(r rune) bool {
		return unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
	}) < 0
}
