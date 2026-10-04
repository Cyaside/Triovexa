package agent

import "github.com/Cyaside/Triovexa/internal/ai/modelgateway"

type RuntimeTrace struct {
	EngineID        string               `json:"engine_id"`
	EngineVersion   string               `json:"engine_version"`
	ContractVersion string               `json:"contract_version"`
	ConfigVersion   string               `json:"config_version"`
	Profile         string               `json:"profile"`
	Accounting      modelgateway.Summary `json:"accounting"`
}
