package runtimebridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/coderepair/agent"
	"github.com/Cyaside/Triovexa/internal/coderepair/sandbox"
	"github.com/Cyaside/Triovexa/internal/security"
)

type Fence func(context.Context) error

// ToolSession is owned by Go, including the candidate and proof. The child
// cannot replace the binding, evidence, test command, campaign or lease.
type ToolSession struct {
	workspace        *sandbox.Workspace
	binding          coderepair.RepositoryBinding
	snapshot         coderepair.EvidenceSnapshot
	recipeID         string
	tests            agent.TestRunner
	fence            Fence
	limits           Limits
	index            *sandbox.SourceIndex
	sourceRevision   string
	readPaths        map[string]bool
	calls            map[string]bool
	repeated         map[string]int
	candidateDigests map[string]bool
	candidates       int
	terminal         bool
	result           agent.InvestigationResult
	receipts         *toolReceipts
}

func NewToolSession(workspace *sandbox.Workspace, binding coderepair.RepositoryBinding,
	snapshot coderepair.EvidenceSnapshot, selection coderepair.AgentSelection, recipeID string,
	tests agent.TestRunner, fence Fence, limits Limits, revision string) (*ToolSession, error) {
	if workspace == nil || tests == nil || fence == nil || !snapshot.VerifyDigest() ||
		!binding.Enabled || binding.ServiceName != snapshot.ServiceName || binding.Environment != snapshot.Environment ||
		!coderepair.ValidGitRevision(revision) || !coderepair.ValidGitRevision(snapshot.DeployedRevision) || limits.MaxToolSteps < 1 || limits.MaxCandidateCount < 1 || limits.MaxCandidateCount > 3 {
		return nil, errors.New("runtime tools require approved scope, fence, tests and a bounded profile")
	}
	return &ToolSession{workspace: workspace, binding: binding, snapshot: snapshot, recipeID: recipeID,
		tests: tests, fence: fence, limits: limits, sourceRevision: revision, index: sandbox.NewSourceIndex(workspace, revision),
		readPaths: make(map[string]bool), calls: make(map[string]bool), repeated: make(map[string]int), candidateDigests: make(map[string]bool),
		result: agent.InvestigationResult{Status: coderepair.StateBlocked, Provider: selection.Provider,
			Model: selection.Model, Prompt: selection.PromptVersion}}, nil
}

func (s *ToolSession) Baseline(ctx context.Context) (Baseline, error) {
	if err := s.fence(ctx); err != nil {
		return Baseline{}, err
	}
	recipe, err := sandbox.ResolveTestRecipe(s.binding, s.recipeID)
	if err != nil || recipe.ExpectedFailure == "" || recipe.ExpectedTestName == "" {
		return Baseline{}, errors.New("registered regression recipe is unavailable")
	}
	var receipt coderepair.ToolReceipt
	if s.receipts != nil {
		receipt, err = s.receipts.beginBaseline(ctx, s)
		if err != nil {
			return Baseline{}, errBaselineReceipt
		}
	}
	before := s.result.Before
	if before.RecipeID == "" {
		before, err = s.tests.Run(ctx, s.workspace.RootPath(), s.binding, s.recipeID)
		s.result.Before = before
	}
	if err != nil || before.Truncated || before.TimedOut {
		return Baseline{}, errors.New("isolated baseline test did not complete")
	}
	if before.RecipeID != s.recipeID || before.ExitCode == 0 || !strings.Contains(before.Output, recipe.ExpectedFailure) || !strings.Contains(before.Output, recipe.ExpectedTestName) {
		return Baseline{}, errors.New("regression baseline does not match the registered bug")
	}
	output := security.Redact(before.Output)
	if len(output) > 4096 {
		output = output[len(output)-4096:]
	}
	baseline := Baseline{ExitCode: before.ExitCode, Output: output, DurationNS: int64(before.Duration)}
	if s.receipts != nil && receipt.State == "started" {
		response, err := canonicalToolResult(ToolResult{CallID: receipt.CallID, Status: "ok", Value: baseline})
		if err != nil || s.receipts.complete(ctx, s, receipt, response) != nil {
			return Baseline{}, errBaselineReceipt
		}
	}
	return baseline, nil
}

func (s *ToolSession) Result() agent.InvestigationResult { return s.result }

func (s *ToolSession) stop(code, reason string, failed bool) ToolResult {
	s.terminal = true
	s.result.Status, s.result.Code, s.result.Reason = coderepair.StateBlocked, code, security.Redact(reason)
	s.clearCandidateProof()
	if failed {
		s.result.Status = coderepair.StateFailed
	}
	return ToolResult{Status: "ok", Value: map[string]any{"terminal": true, "outcome": s.result.Status,
		"code": code, "reason": s.result.Reason}}
}

func (s *ToolSession) clearCandidateProof() {
	s.result.Patch = ""
	s.result.PatchReport = sandbox.PatchReport{}
	s.result.Hypothesis = ""
	s.result.EvidenceIDs = nil
}

func (s *ToolSession) Execute(ctx context.Context, request ToolRequest) ToolResult {
	if s.receipts != nil {
		return s.receipts.execute(ctx, s, request)
	}
	return s.executeResponse(ctx, request)
}

func (s *ToolSession) executeResponse(ctx context.Context, request ToolRequest) ToolResult {
	response := s.execute(ctx, request)
	response.CallID = request.CallID
	return response
}

func (s *ToolSession) execute(ctx context.Context, request ToolRequest) ToolResult {
	if err := s.fence(ctx); err != nil || ctx.Err() != nil {
		return s.stop("LEASE_LOST", "investigation no longer owns its lease", false)
	}
	if s.terminal || request.CallID == "" || len(request.CallID) > 128 || s.calls[request.CallID] {
		return s.stop("PROVIDER_CONTRACT_INVALID", "duplicate call or dispatch after a terminal outcome", true)
	}
	s.calls[request.CallID] = true
	if s.result.Steps >= s.limits.MaxToolSteps {
		return s.stop("STEP_BUDGET", "investigation tool budget was exhausted", false)
	}
	s.result.Steps++
	digest, err := toolArgumentDigest(request)
	if err != nil {
		return s.stop("PROVIDER_CONTRACT_INVALID", "tool arguments are invalid", true)
	}
	hash := sha256.Sum256([]byte(request.Name + ":" + digest))
	key := hex.EncodeToString(hash[:])
	s.repeated[key]++
	if s.repeated[key] > 2 {
		return s.stop("NO_PROGRESS", "repeated tool requests made no progress", false)
	}
	value, err := s.dispatch(ctx, request)
	if err != nil {
		return ToolResult{Status: "error", Code: "TOOL_REJECTED", Message: security.Redact(err.Error())}
	}
	return ToolResult{Status: "ok", Value: value}
}

func (s *ToolSession) dispatch(ctx context.Context, request ToolRequest) (any, error) {
	switch request.Name {
	case "repo_list":
		var args struct {
			Prefix string `json:"prefix"`
			Cursor int    `json:"cursor"`
			Limit  int    `json:"limit"`
		}
		if err := DecodeStrict(request.Args, &args); err != nil {
			return nil, err
		}
		if args.Limit == 0 {
			args.Limit = 20
		}
		paths, next, err := s.index.List(args.Prefix, args.Cursor, args.Limit)
		return map[string]any{"paths": paths, "next_cursor": next, "revision": s.sourceRevision}, err
	case "repo_read":
		var args struct {
			Path           string `json:"path"`
			StartLine      int    `json:"start_line"`
			EndLine        int    `json:"end_line"`
			ExpectedDigest string `json:"expected_digest"`
		}
		if err := DecodeStrict(request.Args, &args); err != nil {
			return nil, err
		}
		result, err := s.index.ReadRange(args.Path, args.StartLine, args.EndLine, args.ExpectedDigest)
		if err == nil {
			s.readPaths[args.Path] = true
		}
		return result, err
	case "repo_search":
		var args struct {
			Prefix string `json:"prefix"`
			Query  string `json:"query"`
			Cursor int    `json:"cursor"`
			Limit  int    `json:"limit"`
		}
		if err := DecodeStrict(request.Args, &args); err != nil {
			return nil, err
		}
		if args.Limit == 0 {
			args.Limit = 20
		}
		hits, next, err := s.index.Search(args.Prefix, args.Query, args.Cursor, args.Limit)
		return map[string]any{"hits": hits, "next_cursor": next, "revision": s.sourceRevision}, err
	case "run_test_recipe":
		var args struct {
			RecipeID        string `json:"recipe_id"`
			CandidateDigest string `json:"candidate_digest"`
		}
		if err := DecodeStrict(request.Args, &args); err != nil {
			return nil, err
		}
		if args.RecipeID != s.recipeID || args.CandidateDigest != s.result.PatchReport.SHA256 {
			return nil, errors.New("test recipe or candidate digest is outside the approved scope")
		}
		// Reuse the immutable baseline instead of running the same test repeatedly.
		return map[string]any{"exit_code": s.result.Before.ExitCode, "output": security.Redact(s.result.Before.Output), "baseline": true}, nil
	case "propose_patch":
		var args struct {
			Patch       string   `json:"patch"`
			Hypothesis  string   `json:"hypothesis"`
			EvidenceIDs []string `json:"evidence_ids"`
		}
		if err := DecodeStrict(request.Args, &args); err != nil {
			return nil, err
		}
		encoded, _ := json.Marshal(agent.Decision{Operation: agent.ProposePatch, Patch: args.Patch, Hypothesis: args.Hypothesis, EvidenceIDs: args.EvidenceIDs})
		decision, err := agent.ParseDecision(string(encoded), s.snapshot)
		if err != nil {
			return nil, err
		}
		if s.candidates >= s.limits.MaxCandidateCount {
			return s.stop("CANDIDATE_BUDGET", "candidate budget was exhausted", false).Value, nil
		}
		patchHash := sha256.Sum256([]byte(strings.TrimSpace(decision.Patch)))
		patchDigest := hex.EncodeToString(patchHash[:])
		if s.candidateDigests[patchDigest] {
			return s.stop("NO_PROGRESS", "a previously rejected patch was proposed again", false).Value, nil
		}
		s.candidates++
		s.candidateDigests[patchDigest] = true
		s.result.After = sandbox.TestResult{}
		s.result.Code, s.result.Reason = "", ""
		s.clearCandidateProof()
		s.result = agent.VerifyCandidate(ctx, s.workspace, s.binding, s.tests, s.recipeID, decision, s.readPaths, s.result)
		if s.result.Status != coderepair.StatePatchReady {
			if s.candidates < s.limits.MaxCandidateCount && recoverableCandidateFailure(s.result.Code) {
				if s.fence(ctx) != nil {
					return s.stop("LEASE_LOST", "investigation no longer owns its lease", false).Value, nil
				}
				restoreCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				err := s.workspace.RestoreCandidate(restoreCtx, s.sourceRevision, s.result.PatchReport)
				cancel()
				if err != nil {
					return s.stop("CANDIDATE_RESTORE_FAILED", "rejected candidate could not be restored to the approved base", true).Value, nil
				}
				accounting := s.index.Accounting()
				s.index = sandbox.NewSourceIndex(s.workspace, s.sourceRevision)
				if err := s.index.RestoreAccounting(accounting); err != nil {
					return s.stop("CHECKPOINT_INCOMPATIBLE", "source accounting could not be preserved", true).Value, nil
				}
				s.clearCandidateProof()
				feedback := map[string]any{"terminal": false, "outcome": "candidate_feedback", "code": s.result.Code,
					"reason": security.Redact(s.result.Reason), "candidate_count": s.candidates,
					"max_candidates": s.limits.MaxCandidateCount, "base_restored": true}
				if s.result.After.RecipeID != "" {
					output := security.Redact(s.result.After.Output)
					if len(output) > 4096 {
						output = output[len(output)-4096:]
					}
					feedback["test"] = map[string]any{"exit_code": s.result.After.ExitCode, "output": output,
						"timed_out": s.result.After.TimedOut, "truncated": s.result.After.Truncated}
				}
				return feedback, nil
			}
			s.clearCandidateProof()
		}
		s.terminal = true
		return map[string]any{"terminal": true, "outcome": s.result.Status, "code": s.result.Code, "reason": s.result.Reason,
			"patch_sha256": s.result.PatchReport.SHA256, "before_exit": s.result.Before.ExitCode, "after_exit": s.result.After.ExitCode}, nil
	case "cannot_determine":
		var args struct {
			Reason string `json:"reason"`
		}
		if err := DecodeStrict(request.Args, &args); err != nil {
			return nil, err
		}
		if args.Reason == "" || len(args.Reason) > 2048 {
			return nil, errors.New("cannot_determine requires a bounded reason")
		}
		return s.stop("CANNOT_DETERMINE", args.Reason, false).Value, nil
	default:
		return s.stop("PROVIDER_CONTRACT_INVALID", "tool is not authorized", true).Value, nil
	}
}

func recoverableCandidateFailure(code string) bool {
	switch code {
	case "INVALID_PATCH", "PATCH_APPLY_FAILED", "FORMAT_FAILED", "TESTS_FAILED":
		return true
	default:
		return false
	}
}

func LeaseFence(store interface {
	GetRepairJob(context.Context, string) (coderepair.Job, error)
	GetRepairCase(context.Context, string) (coderepair.Case, error)
}, claim agent.ClaimedInvestigation) Fence {
	return func(ctx context.Context) error {
		job, err := store.GetRepairJob(ctx, claim.Job.ID)
		if err != nil || job.LeaseToken != claim.Job.LeaseToken || job.Status != coderepair.JobRunning ||
			!job.LeaseUntil.After(time.Now().UTC()) || job.CaseID != claim.Job.CaseID || job.AttemptID != claim.Job.AttemptID {
			return errors.New("repair job lease no longer matches the invocation")
		}
		caseRecord, err := store.GetRepairCase(ctx, job.CaseID)
		if err != nil || caseRecord.State != coderepair.StateInvestigating || caseRecord.Version != claim.ExpectedVersion {
			return errors.New("repair case no longer matches the invocation")
		}
		if safety, ok := store.(interface {
			CheckRepairInvestigationSafety(context.Context, string, time.Time) error
		}); ok {
			if err := safety.CheckRepairInvestigationSafety(ctx, caseRecord.ID, time.Now().UTC()); err != nil {
				return err
			}
		}
		return ctx.Err()
	}
}
