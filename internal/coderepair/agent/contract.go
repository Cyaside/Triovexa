package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode"

	"github.com/Cyaside/Triovexa/internal/coderepair"
)

const maxDecisionBytes = 256 * 1024

type Operation string

const (
	ListFiles       Operation = "list_files"
	ReadFile        Operation = "read_file"
	SearchText      Operation = "search_text"
	ProposePatch    Operation = "propose_patch"
	RunAllowedTest  Operation = "run_allowed_test"
	CannotDetermine Operation = "cannot_determine"
)

// Decision is the entire accepted model protocol. In particular there is no
// command, URL, environment, working-directory or repository-selection field.
type Decision struct {
	Operation   Operation `json:"operation"`
	Path        string    `json:"path,omitempty"`
	Prefix      string    `json:"prefix,omitempty"`
	Query       string    `json:"query,omitempty"`
	Patch       string    `json:"patch,omitempty"`
	RecipeID    string    `json:"recipe_id,omitempty"`
	Hypothesis  string    `json:"hypothesis,omitempty"`
	EvidenceIDs []string  `json:"evidence_ids,omitempty"`
	Reason      string    `json:"reason,omitempty"`
}

// ParseDecision rejects unknown fields, trailing objects and fields that do
// not belong to the selected operation. A model failure is a typed error; it
// never becomes a heuristic action or an unvalidated patch.
func ParseDecision(content string, snapshot coderepair.EvidenceSnapshot) (Decision, error) {
	if content == "" || len(content) > maxDecisionBytes || !snapshot.VerifyDigest() {
		return Decision{}, errors.New("agent decision or evidence snapshot is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(content)))
	decoder.DisallowUnknownFields()
	var d Decision
	if err := decoder.Decode(&d); err != nil {
		return Decision{}, errors.New("agent decision is not a valid operation object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Decision{}, errors.New("agent decision contains trailing content")
	}
	if err := d.validate(snapshot); err != nil {
		return Decision{}, err
	}
	return d, nil
}

func (d Decision) validate(snapshot coderepair.EvidenceSnapshot) error {
	validText := func(value string, max int) bool {
		return len(value) <= max && strings.IndexFunc(value, unicode.IsControl) < 0
	}
	if !validText(d.Path, 512) || !validText(d.Prefix, 512) || !validText(d.Query, 256) ||
		!validText(d.RecipeID, 128) || !validText(d.Hypothesis, 2048) || !validText(d.Reason, 2048) {
		return errors.New("agent decision text exceeds its bounds")
	}
	if len(d.EvidenceIDs) > 20 {
		return errors.New("agent decision cites too many evidence items")
	}
	available := make(map[string]bool, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		available[entry.ID] = entry.Status == coderepair.EvidenceAvailable
	}
	seen := make(map[string]bool, len(d.EvidenceIDs))
	for _, id := range d.EvidenceIDs {
		if !available[id] || seen[id] {
			return errors.New("agent decision cites missing, stale or duplicate evidence")
		}
		seen[id] = true
	}
	switch d.Operation {
	case ListFiles:
		if d.Prefix == "" || d.Path != "" || d.Query != "" || d.Patch != "" || d.RecipeID != "" || d.Reason != "" {
			return errors.New("list_files requires only an allowed prefix")
		}
	case ReadFile:
		if d.Path == "" || d.Prefix != "" || d.Query != "" || d.Patch != "" || d.RecipeID != "" || d.Reason != "" {
			return errors.New("read_file requires only a path")
		}
	case SearchText:
		if d.Prefix == "" || d.Query == "" || d.Path != "" || d.Patch != "" || d.RecipeID != "" || d.Reason != "" {
			return errors.New("search_text requires a prefix and literal query")
		}
	case ProposePatch:
		if d.Patch == "" || d.Hypothesis == "" || len(d.EvidenceIDs) == 0 ||
			d.Path != "" || d.Prefix != "" || d.Query != "" || d.RecipeID != "" || d.Reason != "" {
			return errors.New("propose_patch requires a grounded hypothesis and diff")
		}
	case RunAllowedTest:
		if d.RecipeID == "" || d.Path != "" || d.Prefix != "" || d.Query != "" || d.Patch != "" || d.Reason != "" {
			return errors.New("run_allowed_test requires only a recipe ID")
		}
	case CannotDetermine:
		if d.Reason == "" || d.Path != "" || d.Prefix != "" || d.Query != "" || d.Patch != "" || d.RecipeID != "" {
			return errors.New("cannot_determine requires a reason without an action")
		}
	default:
		return errors.New("agent requested an unknown operation")
	}
	return nil
}
