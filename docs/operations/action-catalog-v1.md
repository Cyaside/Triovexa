# Action Catalog v1

Catalog ini adalah allowlist awal untuk action yang boleh dipertimbangkan sistem.

## Low Risk

### `restart_demo_worker`

- tujuan: memulihkan worker demo non-critical
- environment: `local`, `staging`
- target: `demo-worker`
- approval: wajib
- executable: ya

### `retry_demo_background_job`

- tujuan: me-retry background job demo yang gagal
- environment: `local`, `staging`
- target: `demo-job-runner`
- approval: wajib
- executable: ya

### `refresh_demo_cache`

- tujuan: refresh cache non-critical
- environment: `local`, `staging`
- target: `demo-cache`
- approval: wajib
- executable: ya
- max attempts: `2`

## Medium Risk

### `restart_demo_service`

- tujuan: restart service demo penuh
- environment: `staging`
- target: `demo-api`
- approval: wajib
- executable: belum

### `scale_demo_replicas`

- tujuan: scale replica count dalam batas aman
- environment: `staging`
- target: `demo-api`
- approval: wajib
- executable: belum

### `pause_demo_queue_consumer`

- tujuan: pause consumer untuk membatasi blast radius
- environment: `local`, `staging`
- target: `demo-queue-consumer`
- approval: wajib
- executable: ya
- rollback plan: `resume_demo_queue_consumer`
- max attempts: `1`

### `resume_demo_queue_consumer`

- tujuan: rollback aman untuk melanjutkan queue consumer yang sebelumnya dipause
- environment: `local`, `staging`
- target: `demo-queue-consumer`
- approval: tidak wajib untuk auto-rollback internal
- executable: ya

## High Risk

### `rollback_production_deployment`

- tujuan: rollback deployment production
- environment: `production`
- approval: wajib
- executable: diblok pada fase awal

### `reroute_traffic`

- tujuan: mengalihkan traffic
- environment: `production`
- approval: wajib
- executable: diblok pada fase awal

### `disable_primary_feature_flag`

- tujuan: menonaktifkan feature flag utama
- environment: `production`
- approval: wajib
- executable: diblok pada fase awal
