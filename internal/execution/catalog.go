package execution

import (
	"time"

	"github.com/Cyaside/Triovexa/internal/domain"
)

type ParameterDefinition struct {
	Name        string
	Type        string
	Required    bool
	Description string
}

type ActionDefinition struct {
	Key                  string
	Description          string
	RiskLevel            domain.RiskLevel
	ApprovalRequired     bool
	Executable           bool
	SupportsRollback     bool
	RollbackActionKey    string
	AllowedEnvironments  []string
	AllowedTargets       []string
	MaxExecutionAttempts int
	ExecutionCooldown    time.Duration
	Parameters           []ParameterDefinition
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
			MaxExecutionAttempts: 2,
			ExecutionCooldown:    time.Minute,
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
			MaxExecutionAttempts: 2,
			ExecutionCooldown:    time.Minute,
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
			MaxExecutionAttempts: 2,
			ExecutionCooldown:    time.Minute,
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
			MaxExecutionAttempts: 1,
			ExecutionCooldown:    5 * time.Minute,
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
			MaxExecutionAttempts: 1,
			ExecutionCooldown:    10 * time.Minute,
		},
		"pause_demo_queue_consumer": {
			Key:              "pause_demo_queue_consumer",
			Description:      "Pause queue consumer demo untuk membatasi blast radius.",
			RiskLevel:        domain.RiskLevelMedium,
			ApprovalRequired: true,
			Executable:       true,
			AllowedEnvironments: []string{
				"local",
				"staging",
			},
			AllowedTargets: []string{
				"demo-queue-consumer",
			},
			SupportsRollback:     true,
			RollbackActionKey:    "resume_demo_queue_consumer",
			MaxExecutionAttempts: 1,
			ExecutionCooldown:    5 * time.Minute,
		},
		"resume_demo_queue_consumer": {
			Key:              "resume_demo_queue_consumer",
			Description:      "Resume queue consumer demo sebagai rollback aman untuk consumer pause.",
			RiskLevel:        domain.RiskLevelLow,
			ApprovalRequired: false,
			Executable:       true,
			AllowedEnvironments: []string{
				"local",
				"staging",
			},
			AllowedTargets: []string{
				"demo-queue-consumer",
			},
			MaxExecutionAttempts: 2,
			ExecutionCooldown:    time.Minute,
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
