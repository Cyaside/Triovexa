package runtimebridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

type Process struct {
	Executable string
	Entry      string
	Version    string
}

type cappedLog struct {
	mu     sync.Mutex
	total  int
	cancel context.CancelFunc
}

func (b *cappedLog) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.total += len(p)
	if b.total > 1024*1024 {
		b.cancel()
	}
	return len(p), nil
}

func (b *cappedLog) diagnostics() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	// Stderr is arbitrary child data, including unlabelled secrets and source.
	// Pattern-based redaction cannot make it safe for the incident audit log.
	return fmt.Sprintf("child stderr bytes: %d", b.total)
}

// Run launches one owned child for the entire attempt. It does not invoke a
// shell, inherit provider credentials, or create a new process for each tool.
func (p Process) Run(ctx context.Context, start Start, tools *ToolSession) (ChildResult, error) {
	if p.Executable == "" || !filepath.IsAbs(p.Entry) || p.Version == "" || tools == nil {
		return ChildResult{}, errors.New("agent runtime requires an executable, absolute build entry and engine version")
	}
	if start.Scope.CaseID == "" || start.Scope.AttemptID == "" || start.Scope.EngineVersion != p.Version ||
		start.Scope.CheckpointThread != start.Scope.CaseID+":"+start.Scope.AttemptID {
		return ChildResult{}, errors.New("agent runtime identity is not pinned to the attempt")
	}
	childCtx, stop := context.WithCancel(ctx)
	defer stop()
	command := exec.CommandContext(childCtx, p.Executable, p.Entry)
	command.Dir = filepath.Dir(p.Entry)
	for _, name := range []string{"PATH", "SystemRoot", "WINDIR", "TEMP", "TMP", "TMPDIR"} {
		if value, ok := os.LookupEnv(name); ok {
			command.Env = append(command.Env, name+"="+value)
		}
	}
	command.Env = append(command.Env, "LANGSMITH_TRACING=false", "LANGCHAIN_TRACING_V2=false",
		"LANGCHAIN_TRACING=false", "NODE_OPTIONS=--max-old-space-size=256")
	command.WaitDelay = 3 * time.Second
	stdin, err := command.StdinPipe()
	if err != nil {
		return ChildResult{}, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return ChildResult{}, err
	}
	log := &cappedLog{cancel: stop}
	command.Stderr = log
	if err := command.Start(); err != nil {
		return ChildResult{}, errors.New("agent runtime process could not start")
	}
	defer func() { stop(); _ = stdin.Close(); _ = command.Wait() }()
	frames := make(chan struct {
		frame Frame
		err   error
	}, 1)
	go func() {
		defer close(frames)
		for {
			frame, err := ReadFrame(stdout)
			select {
			case frames <- struct {
				frame Frame
				err   error
			}{frame, err}:
			case <-childCtx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	next := func() (Frame, error) {
		select {
		case <-childCtx.Done():
			return Frame{}, childCtx.Err()
		case read, ok := <-frames:
			if !ok {
				return Frame{}, io.EOF
			}
			return read.frame, read.err
		}
	}
	readyCtx, readyCancel := context.WithTimeout(childCtx, 15*time.Second)
	defer readyCancel()
	var ready Frame
	select {
	case <-readyCtx.Done():
		return ChildResult{}, errors.New("runtime protocol handshake timed out")
	case read := <-frames:
		if read.err != nil {
			return ChildResult{}, errors.New("runtime protocol handshake failed")
		}
		ready = read.frame
	}
	var descriptor struct {
		EngineID        string `json:"engine_id"`
		EngineVersion   string `json:"engine_version"`
		ContractVersion int    `json:"contract_version"`
	}
	if ready.Type != "protocol_ready" || ready.Ordinal != 0 || ready.CaseID != "" || ready.AttemptID != "" ||
		DecodeStrict(ready.Payload, &descriptor) != nil || descriptor.EngineID != "deepagents" ||
		descriptor.EngineVersion != p.Version || descriptor.ContractVersion != 1 {
		return ChildResult{}, errors.New("ENGINE_VERSION_UNAVAILABLE: runtime handshake is incompatible")
	}
	payload, _ := json.Marshal(start)
	outbound := 1
	if err := WriteFrame(stdin, Frame{ContractVersion: ContractVersion, Type: "start_investigation", ID: "start",
		CaseID: start.Scope.CaseID, AttemptID: start.Scope.AttemptID, Ordinal: outbound, Payload: payload}); err != nil {
		return ChildResult{}, err
	}
	inbound := 0
	seen := make(map[string]bool)
	for {
		frame, err := next()
		if err != nil {
			return ChildResult{}, errors.New("runtime ended without an authoritative outcome (" + log.diagnostics() + ")")
		}
		if frame.CaseID != start.Scope.CaseID || frame.AttemptID != start.Scope.AttemptID || frame.Ordinal != inbound+1 || seen[frame.ID] {
			return ChildResult{}, errors.New("runtime frame identity, ordinal or correlation is invalid")
		}
		seen[frame.ID] = true
		inbound++
		if inbound > start.Scope.Limits.MaxToolSteps*3+10 {
			return ChildResult{}, errors.New("runtime frame count exceeds its bound")
		}
		switch frame.Type {
		case "tool_request":
			var request ToolRequest
			if err := DecodeStrict(frame.Payload, &request); err != nil {
				return ChildResult{}, err
			}
			response := tools.Execute(childCtx, request)
			encoded, _ := json.Marshal(response)
			outbound++
			if err := WriteFrame(stdin, Frame{ContractVersion: ContractVersion, Type: "tool_result", ID: frame.ID,
				CaseID: frame.CaseID, AttemptID: frame.AttemptID, Ordinal: outbound, Payload: encoded}); err != nil {
				return ChildResult{}, err
			}
		case "progress":
			// Never log child payloads: they may contain code or provider reasoning.
			if len(frame.Payload) > 4096 {
				return ChildResult{}, errors.New("runtime progress exceeds its bound")
			}
		case "investigation_result":
			var result ChildResult
			if err := DecodeStrict(frame.Payload, &result); err != nil {
				return ChildResult{}, err
			}
			if result.ModelRequests < 0 || result.ModelRequests > start.Scope.Limits.MaxModelRequests || result.ToolSteps < 0 || result.ToolSteps > start.Scope.Limits.MaxToolSteps {
				return ChildResult{}, errors.New("runtime counters exceed their bounds")
			}
			return validatedChildResult(result, tools)
		default:
			return ChildResult{}, errors.New("runtime frame type is unsupported")
		}
	}
}

// Runtime diagnostics describe termination, never authorize success. A child
// cannot place provider errors, source or credentials in public result fields.
func validatedChildResult(result ChildResult, tools *ToolSession) (ChildResult, error) {
	if result.Status != "completed" && result.Status != "blocked" && result.Status != "failed" {
		return ChildResult{}, errors.New("runtime outcome status is unsupported")
	}
	if tools.terminal {
		authoritative := tools.Result()
		result.Status, result.Code, result.Reason = string(authoritative.Status), authoritative.Code, authoritative.Reason
		if authoritative.Status == "patch_ready" {
			result.Status = "completed"
		}
		return result, nil
	}
	if result.Status == "completed" {
		return ChildResult{}, errors.New("runtime success has no Go-verified terminal tool")
	}
	blocked, known := runtimeFailureCodes[result.Code]
	if !known {
		return ChildResult{}, errors.New("runtime outcome code is unsupported")
	}
	result.Status, result.Reason = "failed", "agent runtime stopped before a Go-verified terminal outcome"
	if blocked {
		result.Status = "blocked"
	}
	return result, nil
}

var runtimeFailureCodes = map[string]bool{
	"BUDGET_EXHAUSTED": true, "CONTEXT_LIMIT": true, "CHECKPOINT_INCOMPATIBLE": true,
	"PLAYBOOK_VERSION_UNAVAILABLE": true, "NO_PROGRESS": true, "CANCELLED": true,
	"PROMPT_VERSION_UNAVAILABLE": true, "CANDIDATE_LIMIT": true,
	"DEADLINE_EXCEEDED": true, "NO_TERMINAL_RESULT": true,
	"OFFLINE_EGRESS_DENIED": true, "PRICING_UNKNOWN": true, "BILLING_UNBOUNDED": true,
	"PROVIDER_DISPATCH_UNCERTAIN": true, "USAGE_UNKNOWN": true, "MODEL_DISPATCH_BLOCKED": true,
	"PROVIDER_FAILURE": false, "PROVIDER_CONTRACT_INVALID": false, "PROVIDER_RESPONSE_LIMIT": false,
	"GATEWAY_DENIED": false, "OUTPUT_LIMIT_MISSING": false, "TOOL_INVENTORY_INVALID": false,
	"TOOL_SCHEMA_REPRESENTATION_INVALID": false,
	"CALL_ID_INVALID":                    false, "CALL_ID_MISMATCH": false, "CALL_PAIR_INVALID": false,
	"TOOL_ARGUMENT_INVALID": false, "TOOL_LIMIT": false, "TOOL_TIMEOUT": false,
	"TERMINAL_CALL_MIXED": false, "TERMINAL_ALREADY_REACHED": false, "TERMINAL_CALL_INVALID": false,
	"TERMINAL_RESULT_INVALID": false, "RECIPE_DENIED": false, "CONCURRENT_TOOL": false,
	"CHECKPOINT_IDENTITY_MISMATCH": false, "CHECKPOINT_REQUIRED": false, "CONTRACT_INVALID": false,
	"CHECKPOINT_ROLE_UNSAFE": false, "CHECKPOINT_UNAVAILABLE": false, "CHECKPOINT_DDL_DENIED": false,
	"FRAME_LIMIT": false, "FRAME_TRUNCATED": false, "FRAME_ORDER": false, "PROTOCOL_FAILED": false,
	"IDENTITY_MISMATCH": false, "PARENT_DISCONNECTED": false,
}
