CREATE TABLE repository_bindings (
    id TEXT PRIMARY KEY,
    service_name TEXT NOT NULL,
    environment TEXT NOT NULL,
    repository_url TEXT NOT NULL,
    base_ref TEXT NOT NULL,
    allowed_paths_json JSONB NOT NULL,
    test_recipes_json JSONB NOT NULL,
    policy_version TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE UNIQUE INDEX repository_bindings_active_target_unique
    ON repository_bindings(service_name, environment) WHERE enabled;

CREATE TABLE repair_cases (
    id TEXT PRIMARY KEY,
    incident_id TEXT NOT NULL REFERENCES incidents(id) ON DELETE RESTRICT,
    binding_id TEXT NOT NULL REFERENCES repository_bindings(id) ON DELETE RESTRICT,
    base_sha TEXT NOT NULL,
    deployed_sha TEXT NOT NULL,
    scope_digest TEXT NOT NULL,
    policy_version TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN (
        'proposed', 'awaiting_investigation_approval', 'investigating',
        'patch_ready', 'awaiting_publish_approval', 'publishing', 'pr_open',
        'merged', 'awaiting_deployment', 'verifying', 'recovered',
        'inconclusive', 'blocked', 'failed', 'cancelled', 'closed_without_merge'
    )),
    version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    created_by TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE UNIQUE INDEX repair_cases_one_active_per_incident_binding
    ON repair_cases(incident_id, binding_id)
    WHERE state NOT IN ('recovered', 'failed', 'cancelled', 'closed_without_merge');

CREATE TABLE repair_attempts (
    id TEXT PRIMARY KEY,
    case_id TEXT NOT NULL REFERENCES repair_cases(id) ON DELETE RESTRICT,
    attempt_number INTEGER NOT NULL CHECK (attempt_number > 0),
    status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'blocked', 'failed', 'cancelled')),
    provider TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    prompt_version TEXT NOT NULL DEFAULT '',
    error_code TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    UNIQUE (case_id, attempt_number)
);

CREATE UNIQUE INDEX repair_attempts_one_active_per_case
    ON repair_attempts(case_id) WHERE status IN ('queued', 'running');

CREATE TABLE repair_evidence (
    id TEXT PRIMARY KEY,
    case_id TEXT NOT NULL REFERENCES repair_cases(id) ON DELETE RESTRICT,
    source TEXT NOT NULL,
    observed_at TIMESTAMPTZ NOT NULL,
    complete BOOLEAN NOT NULL,
    content_sha256 TEXT NOT NULL,
    artifact_ref TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE repair_approvals (
    id TEXT PRIMARY KEY,
    case_id TEXT NOT NULL REFERENCES repair_cases(id) ON DELETE RESTRICT,
    case_version BIGINT NOT NULL,
    phase TEXT NOT NULL CHECK (phase IN ('investigation', 'publication')),
    actor_id TEXT NOT NULL,
    decision TEXT NOT NULL CHECK (decision IN ('approved', 'rejected')),
    scope_digest TEXT NOT NULL,
    policy_version TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    UNIQUE (case_id, case_version, phase)
);

CREATE TABLE repair_events (
    id TEXT PRIMARY KEY,
    case_id TEXT NOT NULL REFERENCES repair_cases(id) ON DELETE RESTRICT,
    actor_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    details_json JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX repair_events_case_time ON repair_events(case_id, created_at, id);

CREATE TABLE repair_artifacts (
    id TEXT PRIMARY KEY,
    attempt_id TEXT NOT NULL REFERENCES repair_attempts(id) ON DELETE RESTRICT,
    kind TEXT NOT NULL,
    content_sha256 TEXT NOT NULL,
    artifact_ref TEXT NOT NULL,
    byte_size BIGINT NOT NULL CHECK (byte_size >= 0),
    created_at TIMESTAMPTZ NOT NULL,
    UNIQUE (attempt_id, kind, content_sha256)
);

CREATE TABLE repair_jobs (
    id TEXT PRIMARY KEY,
    case_id TEXT NOT NULL REFERENCES repair_cases(id) ON DELETE RESTRICT,
    attempt_id TEXT NOT NULL REFERENCES repair_attempts(id) ON DELETE RESTRICT,
    type TEXT NOT NULL,
    dedup_key TEXT NOT NULL UNIQUE,
    payload_json JSONB NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'dead_letter')),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    max_attempts INTEGER NOT NULL CHECK (max_attempts > 0),
    available_at TIMESTAMPTZ NOT NULL,
    lease_owner TEXT NOT NULL DEFAULT '',
    lease_token TEXT NOT NULL DEFAULT '',
    lease_until TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX repair_jobs_claim_lookup ON repair_jobs(status, available_at, lease_until);

CREATE TABLE repair_publications (
    id TEXT PRIMARY KEY,
    case_id TEXT NOT NULL UNIQUE REFERENCES repair_cases(id) ON DELETE RESTRICT,
    attempt_id TEXT NOT NULL REFERENCES repair_attempts(id) ON DELETE RESTRICT,
    operation_id TEXT NOT NULL UNIQUE,
    branch_name TEXT NOT NULL,
    head_sha TEXT NOT NULL DEFAULT '',
    pr_number BIGINT,
    pr_url TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE repair_deployments (
    id TEXT PRIMARY KEY,
    case_id TEXT NOT NULL REFERENCES repair_cases(id) ON DELETE RESTRICT,
    environment TEXT NOT NULL,
    revision_sha TEXT NOT NULL,
    deployment_id TEXT NOT NULL UNIQUE,
    observed_at TIMESTAMPTZ NOT NULL,
    verification_status TEXT NOT NULL DEFAULT 'pending',
    created_at TIMESTAMPTZ NOT NULL
);
