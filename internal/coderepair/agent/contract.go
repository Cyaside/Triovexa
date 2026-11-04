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

const ProposePatch Operation = "propose_patch"

// Decision is the internal candidate-proof shape. Native model proposals use
// structured edits; repository tools have their own strict transport schemas.
type Decision struct {
	Operation   Operation                `json:"operation"`
	Patch       string                   `json:"patch,omitempty"`
	Hypothesis  string                   `json:"hypothesis,omitempty"`
	EvidenceIDs []string                 `json:"evidence_ids,omitempty"`
	Summary     coderepair.ChangeSummary `json:"summary"`
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
	if err := d.ValidateGrounding(snapshot); err != nil {
		return Decision{}, err
	}
	if d.Patch == "" {
		return Decision{}, errors.New("candidate proof requires a diff")
	}
	return d, nil
}

// ValidateGrounding runs before diff generation without placeholder patches
// or JSON round trips. The independent patch authority handles diff validity.
func (d Decision) ValidateGrounding(snapshot coderepair.EvidenceSnapshot) error {
	if !snapshot.VerifyDigest() || d.Operation != ProposePatch || d.Hypothesis == "" || len(d.Hypothesis) > 2048 || strings.IndexFunc(d.Hypothesis, unicode.IsControl) >= 0 || len(d.EvidenceIDs) == 0 || len(d.EvidenceIDs) > 20 {
		return errors.New("candidate requires a bounded hypothesis and available incident evidence")
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
	return nil
}
