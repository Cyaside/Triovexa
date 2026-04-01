package topology

type ResourceMetadata struct {
	Target               string
	Service              string
	Owner                string
	Tier                 string
	Dependencies         []string
	ProtectedDownstreams []string
}

type Registry map[string]ResourceMetadata

func DefaultRegistry() Registry {
	return Registry{
		"demo-worker": {
			Target:       "demo-worker",
			Service:      "checkout-worker",
			Owner:        "platform-demo",
			Tier:         "internal",
			Dependencies: []string{"demo-job-runner", "redis-cache"},
		},
		"demo-job-runner": {
			Target:       "demo-job-runner",
			Service:      "checkout-worker",
			Owner:        "platform-demo",
			Tier:         "internal",
			Dependencies: []string{"postgres-demo", "redis-cache"},
		},
		"demo-cache": {
			Target:               "demo-cache",
			Service:              "checkout-api",
			Owner:                "platform-demo",
			Tier:                 "shared",
			Dependencies:         []string{"redis-cache"},
			ProtectedDownstreams: []string{"checkout-api"},
		},
		"demo-api": {
			Target:               "demo-api",
			Service:              "checkout-api",
			Owner:                "platform-demo",
			Tier:                 "customer-facing",
			Dependencies:         []string{"payment-gateway", "inventory-api", "redis-cache"},
			ProtectedDownstreams: []string{"checkout-frontend", "payment-orchestrator"},
		},
		"demo-queue-consumer": {
			Target:               "demo-queue-consumer",
			Service:              "checkout-consumer",
			Owner:                "platform-demo",
			Tier:                 "shared",
			Dependencies:         []string{"payment-gateway", "fraud-check-api", "postgres-demo"},
			ProtectedDownstreams: []string{"order-settlement", "notification-pipeline"},
		},
	}
}

func (r Registry) Get(target string) (ResourceMetadata, bool) {
	metadata, ok := r[target]
	return metadata, ok
}
