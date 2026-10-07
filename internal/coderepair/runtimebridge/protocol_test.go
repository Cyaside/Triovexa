package runtimebridge

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"testing"
)

func TestRuntimeRejectsUnknownContractVersion(t *testing.T) {
	for _, version := range []string{"", "2"} {
		var buffer bytes.Buffer
		if err := WriteFrame(&buffer, Frame{ContractVersion: version, Type: "protocol_ready", ID: "ready", Payload: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadFrame(&buffer); err == nil {
			t.Fatal("accepted incompatible wire contract")
		}
	}
}

func TestFrameRejectsOversizedAndUnknownFields(t *testing.T) {
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], MaxFrameBytes+1)
	if _, err := ReadFrame(bytes.NewReader(header[:])); err == nil {
		t.Fatal("allocated oversized frame")
	}
	for _, body := range []string{`{"contract_version":"1","type":"progress","id":"x","case_id":"c","attempt_id":"a","ordinal":1,"payload":{},"command":"sh"}`, `{} {}`} {
		binary.BigEndian.PutUint32(header[:], uint32(len(body)))
		if _, err := ReadFrame(bytes.NewReader(append(header[:], []byte(body)...))); err == nil {
			t.Fatal("accepted unknown field or trailing JSON")
		}
	}
}

func TestGoTypeScriptSharedContractFixture(t *testing.T) {
	body, err := os.ReadFile("../../../testdata/agent-runtime-contract/start.json")
	if err != nil {
		t.Fatal(err)
	}
	var frame Frame
	if err := DecodeStrict(body, &frame); err != nil {
		t.Fatal(err)
	}
	var start Start
	// Fixture evidence intentionally has a minimal model context rather than
	// a domain snapshot, so validate the common envelope independently here.
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(frame.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	var scope map[string]json.RawMessage
	if err := json.Unmarshal(payload["scope"], &scope); err != nil {
		t.Fatal(err)
	}
	scope["evidence"] = json.RawMessage(`{"incident_id":"i","service_name":"s","environment":"staging","captured_at":"2026-10-03T00:00:00Z","entries":[],"sha256":"dummy"}`)
	payload["scope"], _ = json.Marshal(scope)
	frame.Payload, _ = json.Marshal(payload)
	if err := DecodeStrict(frame.Payload, &start); err != nil {
		t.Fatal(err)
	}
	if frame.ContractVersion != ContractVersion || start.Scope.CheckpointThread != start.Scope.CaseID+":"+start.Scope.AttemptID || start.Scope.EngineID != "deepagents" ||
		start.Scope.PromptVersion != PromptVersion || start.Scope.PlaybookManifestDigest != PlaybookDigest {
		t.Fatal("Go/TS identity contract diverged")
	}
}
