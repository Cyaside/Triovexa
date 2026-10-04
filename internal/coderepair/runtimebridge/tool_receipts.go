package runtimebridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/Cyaside/Triovexa/internal/ai/modelgateway"
	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/coderepair/agent"
	"github.com/Cyaside/Triovexa/internal/coderepair/sandbox"
	"github.com/Cyaside/Triovexa/internal/secretstore"
)

type toolReceipts struct {
	store    coderepair.ToolReceiptStore
	claim    coderepair.ToolReceiptClaim
	cipher   *secretstore.Cipher
	past     map[string]coderepair.ToolReceipt
	replayed map[string]bool
}

type toolSnapshot struct {
	AttemptID        string                    `json:"attempt_id"`
	Revision         string                    `json:"revision"`
	Name             string                    `json:"name"`
	ArgsSHA256       string                    `json:"args_sha256"`
	Response         ToolResult                `json:"response"`
	Result           agent.InvestigationResult `json:"result"`
	ReadPaths        map[string]bool           `json:"read_paths"`
	SourceAccounting sandbox.SourceAccounting  `json:"source_accounting"`
	Repeated         map[string]int            `json:"repeated"`
	Candidates       int                       `json:"candidates"`
	CandidateDigests map[string]bool           `json:"candidate_digests"`
	Terminal         bool                      `json:"terminal"`
}

func (s *ToolSession) RestoreReceipts(ctx context.Context, store coderepair.ToolReceiptStore, claim agent.ClaimedInvestigation, cipher *secretstore.Cipher) error {
	if store == nil || cipher == nil {
		return errors.New("durable tool receipts unavailable")
	}
	r := &toolReceipts{store: store, cipher: cipher, claim: coderepair.ToolReceiptClaim{Job: claim.Job, ExpectedVersion: claim.ExpectedVersion}, past: make(map[string]coderepair.ToolReceipt), replayed: make(map[string]bool)}
	past, err := store.ListRepairToolReceipts(ctx, r.claim)
	if err != nil {
		return err
	}
	var originalBaseline sandbox.TestResult
	hasBaseline := false
	for _, receipt := range past {
		if receipt.AttemptID != claim.Job.AttemptID || receipt.Revision != s.sourceRevision || receipt.State != "completed" {
			return errors.New("TOOL_DISPATCH_UNCERTAIN")
		}
		state, err := r.open(ctx, receipt, s)
		if err != nil {
			return err
		}
		if hasBaseline && state.Result.Before != originalBaseline {
			return errors.New("durable regression baseline changed between tool receipts")
		}
		originalBaseline, hasBaseline = state.Result.Before, true
		s.result, s.readPaths, s.repeated, s.candidates, s.terminal = state.Result, state.ReadPaths, state.Repeated, state.Candidates, state.Terminal
		s.candidateDigests = state.CandidateDigests
		if err := s.index.RestoreAccounting(state.SourceAccounting); err != nil {
			return errors.New("tool receipt source quota is inconsistent")
		}
		r.past[receipt.CallID] = receipt
	}
	s.receipts = r
	return nil
}

func (r *toolReceipts) open(ctx context.Context, receipt coderepair.ToolReceipt, session *ToolSession) (toolSnapshot, error) {
	plain, err := r.cipher.Decrypt(receipt.ResultSealed)
	if err != nil {
		return toolSnapshot{}, errors.New("tool receipt is corrupt")
	}
	var state toolSnapshot
	if len(plain) > 240000 || DecodeStrict([]byte(plain), &state) != nil || state.Response.CallID != receipt.CallID || state.ReadPaths == nil || state.Repeated == nil ||
		state.AttemptID != receipt.AttemptID || state.Revision != receipt.Revision || state.Name != receipt.Name || state.ArgsSHA256 != receipt.ArgsSHA256 {
		return state, errors.New("tool receipt is corrupt")
	}
	if err := validateToolSnapshot(ctx, state, session); err != nil {
		return state, errors.New("tool receipt proof or bounds are invalid")
	}
	if err := session.index.ValidateAccounting(state.SourceAccounting); err != nil {
		return state, errors.New("tool receipt source quota is invalid")
	}
	for path := range state.ReadPaths {
		if _, exists := state.SourceAccounting.Files[path]; !exists {
			return state, errors.New("tool receipt read provenance has no source accounting")
		}
	}
	return state, nil
}

func toolArgumentDigest(request ToolRequest) (string, error) {
	if modelgateway.ValidateUnambiguousJSON(request.Args) != nil {
		return "", errors.New("ambiguous tool arguments")
	}
	decoder := json.NewDecoder(bytes.NewReader(request.Args))
	decoder.UseNumber()
	var args any
	if decoder.Decode(&args) != nil {
		return "", errors.New("invalid tool arguments")
	}
	canonical, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

func (r *toolReceipts) execute(ctx context.Context, s *ToolSession, request ToolRequest) ToolResult {
	blocked := func(code string) ToolResult {
		result := s.stop(code, "Durable tool state could not be safely reconciled.", false)
		result.CallID = request.CallID
		return result
	}
	if s.fence(ctx) != nil {
		return blocked("LEASE_LOST")
	}
	if !validReceiptToolName(request.Name) || !validReceiptCallID(request.CallID) {
		return blocked("PROVIDER_CONTRACT_INVALID")
	}
	digest, err := toolArgumentDigest(request)
	if err != nil {
		return blocked("PROVIDER_CONTRACT_INVALID")
	}
	if past, exists := r.past[request.CallID]; exists {
		if r.replayed[request.CallID] || past.Name != request.Name || past.ArgsSHA256 != digest || past.Revision != s.sourceRevision {
			return blocked("PROVIDER_CONTRACT_INVALID")
		}
		state, err := r.open(ctx, past, s)
		if err != nil {
			return blocked("CHECKPOINT_INCOMPATIBLE")
		}
		r.replayed[request.CallID] = true
		return state.Response
	}
	receipt, err := r.store.StartRepairTool(ctx, r.claim, coderepair.ToolReceipt{AttemptID: r.claim.Job.AttemptID, CallID: request.CallID, Name: request.Name, ArgsSHA256: digest, Revision: s.sourceRevision, State: "started"})
	if err != nil || receipt.State != "started" {
		return blocked("TOOL_DISPATCH_UNCERTAIN")
	}
	response, err := canonicalToolResult(s.executeResponse(ctx, request))
	if err != nil {
		return blocked("CONTEXT_LIMIT")
	}
	if err := r.complete(ctx, s, receipt, response); err != nil {
		return blocked("TOOL_DISPATCH_UNCERTAIN")
	}
	return response
}

func (r *toolReceipts) complete(ctx context.Context, s *ToolSession, receipt coderepair.ToolReceipt, response ToolResult) error {
	encoded, err := json.Marshal(toolSnapshot{AttemptID: receipt.AttemptID, Revision: receipt.Revision, Name: receipt.Name, ArgsSHA256: receipt.ArgsSHA256, Response: response, Result: s.result, ReadPaths: s.readPaths, SourceAccounting: s.index.Accounting(), Repeated: s.repeated, Candidates: s.candidates, CandidateDigests: s.candidateDigests, Terminal: s.terminal})
	if err != nil || len(encoded) > 240000 {
		return errors.New("tool receipt exceeds its bound")
	}
	sealed, err := r.cipher.Encrypt(string(encoded))
	if err != nil {
		return err
	}
	receipt.ResultSealed = sealed
	if err = r.store.CompleteRepairTool(ctx, r.claim, receipt); err != nil {
		return err
	}
	receipt.State = "completed"
	r.past[receipt.CallID] = receipt
	r.replayed[receipt.CallID] = true
	return nil
}

// Canonicalize before the first response is sent, not just on restore. The
// model's serialized transcript must stay identical across a paid-call replay.
func canonicalToolResult(response ToolResult) (ToolResult, error) {
	encoded, err := json.Marshal(response)
	if err != nil {
		return ToolResult{}, err
	}
	var canonical ToolResult
	if err := DecodeStrict(encoded, &canonical); err != nil {
		return ToolResult{}, err
	}
	return canonical, nil
}
