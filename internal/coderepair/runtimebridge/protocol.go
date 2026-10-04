package runtimebridge

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/Cyaside/Triovexa/internal/ai/modelgateway"
	"github.com/Cyaside/Triovexa/internal/coderepair"
)

const ContractVersion = "1"
const MaxFrameBytes = 256 * 1024

type Frame struct {
	ContractVersion string          `json:"contract_version"`
	Type            string          `json:"type"`
	ID              string          `json:"id"`
	CaseID          string          `json:"case_id"`
	AttemptID       string          `json:"attempt_id"`
	Ordinal         int             `json:"ordinal"`
	Payload         json.RawMessage `json:"payload"`
}

type Limits struct {
	MaxModelRequests  int `json:"max_model_requests"`
	MaxToolSteps      int `json:"max_tool_steps"`
	MaxCandidateCount int `json:"max_candidate_count"`
	MaxInputBytes     int `json:"max_input_bytes"`
	MaxContextBytes   int `json:"max_context_bytes"`
	MaxOutputTokens   int `json:"max_output_tokens"`
	RecursionLimit    int `json:"recursion_limit"`
}

type Baseline struct {
	ExitCode   int    `json:"exit_code"`
	Output     string `json:"output"`
	DurationNS int64  `json:"duration_ns"`
}

type Scope struct {
	CaseID                 string                      `json:"case_id"`
	AttemptID              string                      `json:"attempt_id"`
	EngineID               string                      `json:"engine_id"`
	EngineVersion          string                      `json:"engine_version"`
	ContractVersion        int                         `json:"contract_version"`
	CheckpointThread       string                      `json:"checkpoint_thread"`
	Provider               string                      `json:"provider"`
	ProviderConfigVersion  string                      `json:"provider_config_version"`
	PromptVersion          string                      `json:"prompt_version"`
	PlaybookManifestDigest string                      `json:"playbook_manifest_digest"`
	BaseSHA                string                      `json:"base_sha"`
	DeployedSHA            string                      `json:"deployed_sha"`
	AllowedPaths           []string                    `json:"allowed_paths"`
	RecipeIDs              []string                    `json:"recipe_ids"`
	Profile                string                      `json:"profile"`
	Deadline               string                      `json:"deadline"`
	Evidence               coderepair.EvidenceSnapshot `json:"evidence"`
	Baseline               Baseline                    `json:"baseline"`
	Limits                 Limits                      `json:"limits"`
}

type Transport struct {
	Model            string `json:"model"`
	ModelGatewayURL  string `json:"model_gateway_url"`
	Capability       string `json:"capability"`
	CheckpointDSN    string `json:"checkpoint_dsn,omitempty"`
	CheckpointSchema string `json:"checkpoint_schema,omitempty"`
}

type Start struct {
	Scope     Scope     `json:"scope"`
	Transport Transport `json:"transport"`
}

type ToolRequest struct {
	CallID string          `json:"call_id"`
	Name   string          `json:"name"`
	Args   json.RawMessage `json:"args"`
}

type ToolResult struct {
	CallID  string `json:"call_id"`
	Status  string `json:"status"`
	Value   any    `json:"value,omitempty"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

type ChildResult struct {
	Status        string `json:"status"`
	Code          string `json:"code"`
	Reason        string `json:"reason,omitempty"`
	ModelRequests int    `json:"model_requests"`
	ToolSteps     int    `json:"tool_steps"`
}

func DecodeStrict(data []byte, target any) error {
	if modelgateway.ValidateUnambiguousJSON(data) != nil {
		return errors.New("runtime payload contains ambiguous JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("runtime payload does not match the contract")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("runtime payload contains trailing content")
	}
	return nil
}

func ReadFrame(reader io.Reader) (Frame, error) {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return Frame{}, err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > MaxFrameBytes {
		return Frame{}, errors.New("runtime frame exceeds its bound")
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(reader, body); err != nil {
		return Frame{}, err
	}
	var frame Frame
	if err := DecodeStrict(body, &frame); err != nil {
		return Frame{}, err
	}
	if frame.ContractVersion != ContractVersion || frame.ID == "" || frame.Type == "" || frame.Ordinal < 0 || len(frame.Payload) == 0 {
		return Frame{}, errors.New("unsupported or incomplete runtime frame")
	}
	return frame, nil
}

func WriteFrame(writer io.Writer, frame Frame) error {
	body, err := json.Marshal(frame)
	if err != nil || len(body) == 0 || len(body) > MaxFrameBytes {
		return errors.New("runtime frame cannot be encoded within its bound")
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(body)))
	if _, err := writer.Write(header[:]); err != nil {
		return err
	}
	_, err = writer.Write(body)
	return err
}

func DefaultLimits() Limits {
	return Limits{MaxModelRequests: 4, MaxToolSteps: 20, MaxCandidateCount: 1,
		MaxInputBytes: 24000, MaxContextBytes: 16000, MaxOutputTokens: 1500, RecursionLimit: 30}
}

func Deadline(ctxDeadline time.Time) string { return ctxDeadline.UTC().Format(time.RFC3339Nano) }
