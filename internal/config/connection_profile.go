package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
)

const (
	ReasoningConnectionSettingKey       = "connection.reasoning"
	ReasoningConnectionBundleSettingKey = "connection.reasoning.bundle.v1"
	GrafanaConnectionSettingKey         = "connection.grafana"
)

var credentialReferencePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,127}$`)

// ReasoningConnectionProfile contains only non-secret provider configuration.
// CredentialRef names an environment variable; its value is never persisted.
type ReasoningConnectionProfile struct {
	Provider      string `json:"provider"`
	BaseURL       string `json:"base_url"`
	Model         string `json:"model"`
	CredentialRef string `json:"credential_ref"`
	JSONMode      bool   `json:"json_mode"`
}

// ReasoningConnectionBundle stores non-secret provider configuration together
// with an encrypted API key. The key required to decrypt it is kept outside the
// database.
type ReasoningConnectionBundle struct {
	Profile           ReasoningConnectionProfile `json:"profile"`
	EncryptedAPIKey   string                     `json:"encrypted_api_key"`
	EncryptionVersion int                        `json:"encryption_version"`
}

// GrafanaConnectionProfile contains non-secret Grafana and query configuration.
type GrafanaConnectionProfile struct {
	BaseURL          string `json:"base_url"`
	CredentialRef    string `json:"credential_ref"`
	MetricsSourceUID string `json:"metrics_source_uid"`
	LogsSourceUID    string `json:"logs_source_uid"`
	ErrorRateQuery   string `json:"error_rate_query"`
	LatencyQuery     string `json:"latency_query"`
	QueueQuery       string `json:"queue_query"`
	ReplicaQuery     string `json:"replica_query"`
	LogsQuery        string `json:"logs_query"`
	DeployLogsQuery  string `json:"deploy_logs_query"`
}

func EncodeConnectionProfile(value any) (string, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode connection profile: %w", err)
	}
	return string(body), nil
}

func DecodeReasoningConnectionProfile(raw string) (ReasoningConnectionProfile, error) {
	var profile ReasoningConnectionProfile
	if err := json.Unmarshal([]byte(raw), &profile); err != nil {
		return profile, fmt.Errorf("decode reasoning connection profile: %w", err)
	}
	profile.Provider = strings.ToLower(strings.TrimSpace(profile.Provider))
	profile.BaseURL = strings.TrimRight(strings.TrimSpace(profile.BaseURL), "/")
	profile.Model = strings.TrimSpace(profile.Model)
	profile.CredentialRef = strings.TrimSpace(profile.CredentialRef)
	return profile, ValidateReasoningConnectionProfile(profile)
}

func DecodeReasoningConnectionBundle(raw string) (ReasoningConnectionBundle, error) {
	var bundle ReasoningConnectionBundle
	if err := json.Unmarshal([]byte(raw), &bundle); err != nil {
		return bundle, fmt.Errorf("decode reasoning connection bundle: %w", err)
	}
	profileRaw, err := json.Marshal(bundle.Profile)
	if err != nil {
		return bundle, fmt.Errorf("normalize reasoning connection bundle: %w", err)
	}
	bundle.Profile, err = DecodeReasoningConnectionProfile(string(profileRaw))
	if err != nil {
		return bundle, err
	}
	bundle.EncryptedAPIKey = strings.TrimSpace(bundle.EncryptedAPIKey)
	if bundle.EncryptedAPIKey == "" || bundle.EncryptionVersion != 1 {
		return bundle, fmt.Errorf("reasoning connection bundle is incomplete")
	}
	return bundle, nil
}

func DecodeGrafanaConnectionProfile(raw string) (GrafanaConnectionProfile, error) {
	var profile GrafanaConnectionProfile
	if err := json.Unmarshal([]byte(raw), &profile); err != nil {
		return profile, fmt.Errorf("decode Grafana connection profile: %w", err)
	}
	profile.BaseURL = strings.TrimRight(strings.TrimSpace(profile.BaseURL), "/")
	profile.CredentialRef = strings.TrimSpace(profile.CredentialRef)
	profile.MetricsSourceUID = strings.TrimSpace(profile.MetricsSourceUID)
	profile.LogsSourceUID = strings.TrimSpace(profile.LogsSourceUID)
	return profile, ValidateGrafanaConnectionProfile(profile)
}

func ValidateReasoningConnectionProfile(profile ReasoningConnectionProfile) error {
	if profile.Provider != "openai-compatible" {
		return fmt.Errorf("provider must be openai-compatible")
	}
	if err := validateHTTPURL(profile.BaseURL); err != nil {
		return fmt.Errorf("invalid provider base URL: %w", err)
	}
	if strings.TrimSpace(profile.Model) == "" {
		return fmt.Errorf("model is required")
	}
	return ValidateCredentialReference(profile.CredentialRef)
}

func ValidateGrafanaConnectionProfile(profile GrafanaConnectionProfile) error {
	if err := validateHTTPURL(profile.BaseURL); err != nil {
		return fmt.Errorf("invalid Grafana base URL: %w", err)
	}
	if err := ValidateCredentialReference(profile.CredentialRef); err != nil {
		return err
	}
	if profile.MetricsSourceUID == "" || profile.LogsSourceUID == "" {
		return fmt.Errorf("metrics and logs datasource UIDs are required")
	}
	return nil
}

func ValidateCredentialReference(reference string) error {
	if !credentialReferencePattern.MatchString(strings.TrimSpace(reference)) {
		return fmt.Errorf("credential reference must be an environment variable name")
	}
	return nil
}

func ResolveCredential(reference string) (string, bool) {
	if ValidateCredentialReference(reference) != nil {
		return "", false
	}
	value, ok := os.LookupEnv(strings.TrimSpace(reference))
	value = strings.TrimSpace(value)
	return value, ok && value != ""
}

func ApplyReasoningConnectionProfile(cfg *Config, profile ReasoningConnectionProfile) {
	cfg.LLMBaseURL = profile.BaseURL
	cfg.LLMModel = profile.Model
	cfg.LLMJSONMode = profile.JSONMode
	if credential, ok := ResolveCredential(profile.CredentialRef); ok {
		cfg.LLMAPIKey = credential
	} else {
		cfg.LLMAPIKey = ""
	}
}

func ApplyGrafanaConnectionProfile(cfg *Config, profile GrafanaConnectionProfile) {
	cfg.GrafanaBaseURL = profile.BaseURL
	cfg.GrafanaMetricsSourceUID = profile.MetricsSourceUID
	cfg.GrafanaLogsSourceUID = profile.LogsSourceUID
	cfg.GrafanaErrorRateQuery = profile.ErrorRateQuery
	cfg.GrafanaLatencyQuery = profile.LatencyQuery
	cfg.GrafanaQueueQuery = profile.QueueQuery
	cfg.GrafanaReplicaQuery = profile.ReplicaQuery
	cfg.GrafanaLogsQuery = profile.LogsQuery
	cfg.GrafanaDeployLogsQuery = profile.DeployLogsQuery
	if credential, ok := ResolveCredential(profile.CredentialRef); ok {
		cfg.GrafanaAPIToken = credential
	} else {
		cfg.GrafanaAPIToken = ""
	}
}

func validateHTTPURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("must be an absolute HTTP(S) URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("credentials, query parameters, and fragments are not allowed")
	}
	return nil
}
