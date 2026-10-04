package runtimebridge

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/coderepair/agent"
)

// This child implements only framed IPC. It has no model client, network access
// or dependency installation; the tests exercise the actual process boundary.
func scriptedProcess(t *testing.T, action string) Process {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required for the offline process-boundary fixture")
	}
	entry := filepath.Join(t.TempDir(), "fixture.cjs")
	version, _ := json.Marshal(EngineVersion)
	script := `function emit(type, payload, ordinal = 0, id = "fixture-" + ordinal) {
	  const body = Buffer.from(JSON.stringify({contract_version: "1", type, id,
    case_id: ordinal ? "case" : "", attempt_id: ordinal ? "attempt" : "", ordinal, payload}));
  const header = Buffer.alloc(4); header.writeUInt32BE(body.length);
  process.stdout.write(Buffer.concat([header, body]));
}
emit("protocol_ready", {engine_id: "deepagents", engine_version: ` + string(version) + `, contract_version: 1});
process.stdin.once("data", () => { ` + action + ` });
`
	if err := os.WriteFile(entry, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	return Process{Executable: node, Entry: entry, Version: EngineVersion}
}

func processScope() Start {
	return Start{Scope: Scope{CaseID: "case", AttemptID: "attempt", EngineVersion: EngineVersion,
		CheckpointThread: "case:attempt", Limits: DefaultLimits()}}
}

func TestProcessRejectsForgedSuccessOverIPC(t *testing.T) {
	process := scriptedProcess(t, `emit("investigation_result", {status: "completed", code: "PATCH_VERIFIED", model_requests: 0, tool_steps: 0}, 1);`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := process.Run(ctx, processScope(), &ToolSession{}); err == nil || !strings.Contains(err.Error(), "no Go-verified terminal tool") {
		t.Fatalf("child success bypassed Go proof: %v", err)
	}
}

func TestProcessNeverReturnsUntrustedDiagnostics(t *testing.T) {
	const private = "private-unlabelled-material-7e8b17"
	encoded, _ := json.Marshal(private)
	for _, scenario := range []struct {
		name   string
		action string
	}{
		{"stderr", `process.stderr.write(` + string(encoded) + `, () => { process.stdout.end(() => process.exit(1)); });`},
		{"frame type", `emit(` + string(encoded) + `, {}, 1);`},
		{"outcome code", `emit("investigation_result", {status: "failed", code: ` + string(encoded) + `, reason: ` + string(encoded) + `, model_requests: 0, tool_steps: 0}, 1);`},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			process := scriptedProcess(t, scenario.action)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result, err := process.Run(ctx, processScope(), &ToolSession{})
			if err == nil || strings.Contains(err.Error(), private) || strings.Contains(result.Reason, private) || strings.Contains(result.Code, private) {
				t.Fatalf("untrusted child data reached diagnostics: result=%+v err=%v", result, err)
			}
		})
	}
}

func TestProcessFailureReasonIsFixedAndProviderCredentialsAreNotInherited(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "synthetic-parent-secret")
	t.Setenv("PRIVATE_UNLABELLED_RUNTIME_SECRET", "synthetic-parent-secret")
	process := scriptedProcess(t, `emit("investigation_result", {status: "failed",
  code: process.env.OPENAI_API_KEY || process.env.PRIVATE_UNLABELLED_RUNTIME_SECRET ? "CREDENTIALS_INHERITED" : "CONTEXT_LIMIT",
  reason: "arbitrary provider response and private source", model_requests: 0, tool_steps: 0}, 1);`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := process.Run(ctx, processScope(), &ToolSession{})
	if err != nil || result.Status != "blocked" || result.Code != "CONTEXT_LIMIT" || result.Reason != "agent runtime stopped before a Go-verified terminal outcome" {
		t.Fatalf("runtime reason or inherited environment was accepted: result=%+v err=%v", result, err)
	}
}

func TestStderrLimitCancelsChildAndRetainsOnlySize(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := &cappedLog{cancel: cancel}
	secret := strings.Repeat("private-unlabelled-source\n", 50000)
	if n, err := log.Write([]byte(secret)); err != nil || n != len(secret) {
		t.Fatalf("stderr write: n=%d err=%v", n, err)
	}
	if ctx.Err() == nil || strings.Contains(log.diagnostics(), "private") || len(log.diagnostics()) > 64 {
		t.Fatal("stderr bound did not cancel or retained private child content")
	}
}

func TestValidatedOutcomeUsesGoTerminalProof(t *testing.T) {
	tools := &ToolSession{terminal: true, result: agent.InvestigationResult{Status: coderepair.StateFailed,
		Code: "UNGROUNDED_PATCH", Reason: "candidate changed a file that was never read"}}
	result, err := validatedChildResult(ChildResult{Status: "completed", Code: "PATCH_VERIFIED", Reason: "forged success"}, tools)
	if err != nil || result.Status != "failed" || result.Code != "UNGROUNDED_PATCH" || result.Reason == "forged success" {
		t.Fatalf("child replaced the Go terminal outcome: %+v %v", result, err)
	}
}

func TestValidatedOutcomeRejectsUnknownStatusAndSuccessCode(t *testing.T) {
	for _, result := range []ChildResult{
		{Status: "patch_ready", Code: "PROVIDER_FAILURE"},
		{Status: "completed", Code: "PATCH_VERIFIED"},
		{Status: "blocked", Code: "PATCH_VERIFIED"},
		{Status: "failed", Code: "invented diagnostic"},
	} {
		if _, err := validatedChildResult(result, &ToolSession{}); err == nil {
			t.Fatalf("accepted unverified child result: %+v", result)
		}
	}
}

func TestRuntimeCancellationStopsOwnedChild(t *testing.T) {
	heartbeat := filepath.Join(t.TempDir(), "owned-child-heartbeat")
	encoded, _ := json.Marshal(heartbeat)
	process := scriptedProcess(t, `const fs = require("node:fs"); setInterval(() => fs.appendFileSync(`+string(encoded)+`, "."), 20);`)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := process.Run(ctx, processScope(), &ToolSession{}); err == nil {
		t.Fatal("cancelled child returned a successful result")
	}
	before, err := os.ReadFile(heartbeat)
	if err != nil || len(before) == 0 {
		t.Fatal("child fixture did not run before cancellation")
	}
	time.Sleep(100 * time.Millisecond)
	after, err := os.ReadFile(heartbeat)
	if err != nil || string(after) != string(before) {
		t.Fatal("owned Node process remained active after cancellation")
	}
}

func TestProcessRejectsOutOfOrderAndCrossAttemptFrames(t *testing.T) {
	for _, action := range []string{
		`emit("tool_request", {call_id:"read",name:"repo_read",args:{}}, 2);`,
		`const body = Buffer.from(JSON.stringify({contract_version:"1",type:"tool_request",id:"bad",case_id:"case",attempt_id:"other-attempt",ordinal:1,payload:{call_id:"read",name:"repo_read",args:{}}})); const header=Buffer.alloc(4);header.writeUInt32BE(body.length);process.stdout.write(Buffer.concat([header,body]));`,
	} {
		process := scriptedProcess(t, action)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		tools := &ToolSession{}
		_, err := process.Run(ctx, processScope(), tools)
		cancel()
		if err == nil || !strings.Contains(err.Error(), "frame identity, ordinal or correlation") || tools.result.Steps != 0 {
			t.Fatalf("unscoped frame reached a tool: %v", err)
		}
	}
}

func TestProcessRejectsDuplicateCorrelationBeforeAnyTool(t *testing.T) {
	process := scriptedProcess(t, `emit("progress", {}, 1, "same-correlation");
emit("progress", {}, 2, "same-correlation");
emit("tool_request", {call_id:"read",name:"repo_read",args:{}}, 3);`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tools := &ToolSession{}
	_, err := process.Run(ctx, processScope(), tools)
	if err == nil || !strings.Contains(err.Error(), "frame identity, ordinal or correlation") || tools.result.Steps != 0 {
		t.Fatalf("duplicate correlation reached a tool: steps=%d err=%v", tools.result.Steps, err)
	}
}
