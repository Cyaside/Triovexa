package agent

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/Cyaside/Triovexa/internal/ai"
	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/security"
)

func (r InvestigationResult) Outcome(job coderepair.Job, expectedVersion int64, recipeID string, now time.Time) (coderepair.InvestigationOutcome, error) {
	if job.ID == "" || job.CaseID == "" || job.AttemptID == "" || job.LeaseToken == "" || expectedVersion < 1 ||
		recipeID == "" || now.IsZero() {
		return coderepair.InvestigationOutcome{}, errors.New("investigation outcome has no claimed job scope")
	}
	if r.Status != coderepair.StatePatchReady && r.Status != coderepair.StateBlocked && r.Status != coderepair.StateFailed {
		return coderepair.InvestigationOutcome{}, errors.New("investigation returned no final state")
	}
	report, err := json.Marshal(struct {
		Status      coderepair.State   `json:"status"`
		RecipeID    string             `json:"recipe_id"`
		PatchSHA256 string             `json:"patch_sha256,omitempty"`
		Hypothesis  string             `json:"hypothesis,omitempty"`
		EvidenceIDs []string           `json:"evidence_ids,omitempty"`
		BeforeExit  int                `json:"before_exit"`
		AfterExit   int                `json:"after_exit"`
		BeforeNS    int64              `json:"before_duration_ns"`
		AfterNS     int64              `json:"after_duration_ns"`
		BeforeText  string             `json:"before_output,omitempty"`
		AfterText   string             `json:"after_output,omitempty"`
		Usage       ai.CompletionUsage `json:"usage"`
		Provider    string             `json:"provider"`
		Model       string             `json:"model"`
		Prompt      string             `json:"prompt_version"`
		Steps       int                `json:"steps"`
		Code        string             `json:"code"`
		Reason      string             `json:"reason,omitempty"`
		Runtime     *RuntimeTrace      `json:"runtime,omitempty"`
	}{r.Status, recipeID, r.PatchReport.SHA256, security.Redact(r.Hypothesis), r.EvidenceIDs,
		r.Before.ExitCode, r.After.ExitCode, int64(r.Before.Duration), int64(r.After.Duration),
		security.Redact(r.Before.Output), security.Redact(r.After.Output), r.Usage,
		r.Provider, r.Model, r.Prompt, r.Steps, r.Code, security.Redact(r.Reason), r.Runtime})
	if err != nil {
		return coderepair.InvestigationOutcome{}, err
	}
	outcome := coderepair.InvestigationOutcome{
		JobID: job.ID, LeaseToken: job.LeaseToken, CaseID: job.CaseID, AttemptID: job.AttemptID,
		ExpectedVersion: expectedVersion, State: r.Status, PatchSHA256: r.PatchReport.SHA256,
		ReportJSON: report, RecordedAt: now.UTC(),
	}
	if r.Status == coderepair.StatePatchReady {
		if r.Patch == "" || r.Before.ExitCode == 0 || r.After.ExitCode != 0 {
			return coderepair.InvestigationOutcome{}, errors.New("patch-ready result is missing red-to-green proof")
		}
		outcome.Patch = []byte(r.Patch)
	} else {
		outcome.ErrorCode = r.Code
		outcome.ErrorMessage = security.Redact(r.Reason)
	}
	return outcome, nil
}
