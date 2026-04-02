# Action Catalog v1

This catalog is the baseline allowlist of actions the system may consider.

## Low Risk

### `restart_demo_worker`

- purpose: restore a non-critical demo worker
- environment: `local`, `staging`
- target: `demo-worker`
- approval: required
- executable: yes

### `retry_demo_background_job`

- purpose: retry a failed demo background job
- environment: `local`, `staging`
- target: `demo-job-runner`
- approval: required
- executable: yes

### `refresh_demo_cache`

- purpose: refresh a non-critical cache
- environment: `local`, `staging`
- target: `demo-cache`
- approval: required
- executable: yes
- max attempts: `2`

## Medium Risk

### `restart_demo_service`

- purpose: restart the full demo service
- environment: `staging`
- target: `demo-api`
- approval: required
- executable: not yet

### `scale_demo_replicas`

- purpose: scale replica count within a safe range
- environment: `staging`
- target: `demo-api`
- approval: required
- executable: not yet

### `pause_demo_queue_consumer`

- purpose: pause the consumer to limit blast radius
- environment: `local`, `staging`
- target: `demo-queue-consumer`
- approval: required
- executable: yes
- rollback plan: `resume_demo_queue_consumer`
- max attempts: `1`

### `resume_demo_queue_consumer`

- purpose: safely resume a queue consumer that was previously paused
- environment: `local`, `staging`
- target: `demo-queue-consumer`
- approval: not required for internal auto-rollback
- executable: yes

## High Risk

### `rollback_production_deployment`

- purpose: roll back a production deployment
- environment: `production`
- approval: required
- executable: blocked

### `reroute_traffic`

- purpose: reroute live traffic
- environment: `production`
- approval: required
- executable: blocked

### `disable_primary_feature_flag`

- purpose: disable a primary feature flag
- environment: `production`
- approval: required
- executable: blocked
