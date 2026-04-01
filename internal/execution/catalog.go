package execution

import "github.com/Cyaside/Triovexa/internal/domain"

type ParameterDefinition struct {
	Name        string
	Type        string
	Required    bool
	Description string
}

type ActionDefinition struct {
	Key                 string
	Description         string
	RiskLevel           domain.RiskLevel
	ApprovalRequired    bool
	Executable          bool
	SupportsRollback    bool
	AllowedEnvironments []string
	AllowedTargets      []string
	Parameters          []ParameterDefinition
}

type Catalog map[string]ActionDefinition

func DefaultCatalog() Catalog {
	return Catalog{
		"restart_demo_worker": {
			Key:              "restart_demo_worker",
			Description:      "Restart satu worker demo non-critical untuk memulihkan background processing.",
			RiskLevel:        domain.RiskLevelLow,
			ApprovalRequired: true,
			Executable:       true,
			AllowedEnvironments: []string{
				"local",
				"staging",
			},
			AllowedTargets: []string{
				"demo-worker",
			},
			Parameters: []ParameterDefinition{
				{Name: "worker_id", Type: "string", Required: true, Description: "Identifier worker demo yang boleh direstart."},
			},
		},
		"retry_demo_background_job": {
			Key:              "retry_demo_background_job",
			Description:      "Retry background job demo yang gagal tanpa mengubah data kritikal.",
			RiskLevel:        domain.RiskLevelLow,
			ApprovalRequired: true,
			Executable:       true,
			AllowedEnvironments: []string{
				"local",
				"staging",
			},
			AllowedTargets: []string{
				"demo-job-runner",
			},
			Parameters: []ParameterDefinition{
				{Name: "job_id", Type: "string", Required: true, Description: "Identifier job yang akan di-retry."},
			},
		},
		"refresh_demo_cache": {
			Key:              "refresh_demo_cache",
			Description:      "Refresh cache non-critical pada service demo.",
			RiskLevel:        domain.RiskLevelLow,
			ApprovalRequired: true,
			Executable:       true,
			AllowedEnvironments: []string{
				"local",
				"staging",
			},
			AllowedTargets: []string{
				"demo-cache",
			},
			Parameters: []ParameterDefinition{
				{Name: "cache_key", Type: "string", Required: false, Description: "Opsional untuk refresh cache key tertentu."},
			},
		},
		"restart_demo_service": {
			Key:              "restart_demo_service",
			Description:      "Restart service demo penuh. Disiapkan untuk phase medium-risk berikutnya.",
			RiskLevel:        domain.RiskLevelMedium,
			ApprovalRequired: true,
			Executable:       false,
			AllowedEnvironments: []string{
				"staging",
			},
			AllowedTargets: []string{
				"demo-api",
			},
		},
		"scale_demo_replicas": {
			Key:              "scale_demo_replicas",
			Description:      "Scale replica count service demo dalam batas terbatas.",
			RiskLevel:        domain.RiskLevelMedium,
			ApprovalRequired: true,
			Executable:       false,
			AllowedEnvironments: []string{
				"staging",
			},
			AllowedTargets: []string{
				"demo-api",
			},
			Parameters: []ParameterDefinition{
				{Name: "replicas", Type: "int", Required: true, Description: "Jumlah replica tujuan dalam batas aman."},
			},
		},
		"pause_demo_queue_consumer": {
			Key:              "pause_demo_queue_consumer",
			Description:      "Pause queue consumer demo untuk membatasi blast radius.",
			RiskLevel:        domain.RiskLevelMedium,
			ApprovalRequired: true,
			Executable:       false,
			AllowedEnvironments: []string{
				"staging",
			},
			AllowedTargets: []string{
				"demo-queue-consumer",
			},
			SupportsRollback: true,
		},
		"rollback_production_deployment": {
			Key:              "rollback_production_deployment",
			Description:      "Rollback deployment production. Tetap diblok pada fase awal.",
			RiskLevel:        domain.RiskLevelHigh,
			ApprovalRequired: true,
			Executable:       false,
			AllowedEnvironments: []string{
				"production",
			},
			AllowedTargets: []string{
				"demo-api",
			},
			SupportsRollback: true,
		},
		"reroute_traffic": {
			Key:              "reroute_traffic",
			Description:      "Reroute traffic antar target service. Disediakan sebagai referensi high-risk.",
			RiskLevel:        domain.RiskLevelHigh,
			ApprovalRequired: true,
			Executable:       false,
			AllowedEnvironments: []string{
				"production",
			},
			AllowedTargets: []string{
				"edge-router",
			},
		},
		"disable_primary_feature_flag": {
			Key:              "disable_primary_feature_flag",
			Description:      "Disable feature flag utama. Tetap diblok pada fase awal.",
			RiskLevel:        domain.RiskLevelHigh,
			ApprovalRequired: true,
			Executable:       false,
			AllowedEnvironments: []string{
				"production",
			},
			AllowedTargets: []string{
				"primary-checkout-flag",
			},
		},
	}
}

func (c Catalog) Get(key string) (ActionDefinition, bool) {
	definition, ok := c[key]
	return definition, ok
}
