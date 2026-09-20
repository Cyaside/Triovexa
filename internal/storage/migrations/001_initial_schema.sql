CREATE TABLE IF NOT EXISTS incidents (
    id TEXT PRIMARY KEY,
    external_alert_id TEXT NOT NULL,
    alert_source TEXT NOT NULL,
    title TEXT NOT NULL,
    service_name TEXT NOT NULL,
    environment TEXT NOT NULL,
    severity TEXT NOT NULL,
    state TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS audit_events (
    id TEXT PRIMARY KEY,
    incident_id TEXT NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
    step_name TEXT NOT NULL,
    status TEXT NOT NULL,
    details_json JSONB NOT NULL,
    started_at TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS evidence_items (
    id TEXT PRIMARY KEY,
    incident_id TEXT NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
    type TEXT NOT NULL,
    source TEXT NOT NULL,
    snippet TEXT NOT NULL,
    timestamp TIMESTAMPTZ NOT NULL,
    metadata_json JSONB NOT NULL
);

CREATE TABLE IF NOT EXISTS document_references (
    id TEXT PRIMARY KEY,
    incident_id TEXT NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
    document_title TEXT NOT NULL,
    document_type TEXT NOT NULL,
    relevance_reason TEXT NOT NULL,
    snippet TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS triage_results (
    id TEXT PRIMARY KEY,
    incident_id TEXT NOT NULL UNIQUE REFERENCES incidents(id) ON DELETE CASCADE,
    summary TEXT NOT NULL,
    hypotheses_json JSONB NOT NULL,
    blast_radius TEXT NOT NULL,
    next_steps_json JSONB NOT NULL,
    draft_status_update TEXT NOT NULL,
    confidence_notes TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS candidate_actions (
    id TEXT PRIMARY KEY,
    incident_id TEXT NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
    action_type TEXT NOT NULL,
    target_resource TEXT NOT NULL,
    parameters_json JSONB NOT NULL,
    risk_level TEXT NOT NULL,
    rationale TEXT NOT NULL,
    evidence_refs_json JSONB NOT NULL,
    approval_hint TEXT NOT NULL,
    status TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS policy_decisions (
    id TEXT PRIMARY KEY,
    candidate_action_id TEXT NOT NULL UNIQUE REFERENCES candidate_actions(id) ON DELETE CASCADE,
    decision TEXT NOT NULL,
    reason TEXT NOT NULL,
    approval_required BOOLEAN NOT NULL,
    policy_rule_ref TEXT NOT NULL,
    decided_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS approval_records (
    id TEXT PRIMARY KEY,
    candidate_action_id TEXT NOT NULL REFERENCES candidate_actions(id) ON DELETE CASCADE,
    approved_by TEXT NOT NULL,
    decision TEXT NOT NULL,
    note TEXT NOT NULL,
    action_digest TEXT NOT NULL DEFAULT '',
    policy_version TEXT NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS execution_records (
    id TEXT PRIMARY KEY,
    candidate_action_id TEXT NOT NULL REFERENCES candidate_actions(id) ON DELETE CASCADE,
    idempotency_key TEXT NOT NULL,
    initiated_by TEXT NOT NULL,
    executor_type TEXT NOT NULL,
    status TEXT NOT NULL,
    started_at TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ NOT NULL,
    result_json JSONB NOT NULL
);

CREATE TABLE IF NOT EXISTS workflow_jobs (
    id TEXT PRIMARY KEY,
    type TEXT NOT NULL,
    dedup_key TEXT NOT NULL UNIQUE,
    payload_json JSONB NOT NULL,
    status TEXT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 3,
    available_at TIMESTAMPTZ NOT NULL,
    lease_owner TEXT NOT NULL DEFAULT '',
    lease_until TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS users (
    id TEXT PRIMARY KEY,
    username TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    role TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
    token_hash TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    csrf_hash TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS application_settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS verification_results (
    id TEXT PRIMARY KEY,
    execution_record_id TEXT NOT NULL UNIQUE REFERENCES execution_records(id) ON DELETE CASCADE,
    status TEXT NOT NULL,
    evidence_json JSONB NOT NULL,
    notes TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS rollback_records (
    id TEXT PRIMARY KEY,
    candidate_action_id TEXT NOT NULL REFERENCES candidate_actions(id) ON DELETE CASCADE,
    rollback_action_key TEXT NOT NULL,
    triggered_by TEXT NOT NULL,
    status TEXT NOT NULL,
    started_at TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ NOT NULL,
    result_json JSONB NOT NULL,
    note TEXT NOT NULL
);

ALTER TABLE approval_records ADD COLUMN IF NOT EXISTS action_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE approval_records ADD COLUMN IF NOT EXISTS policy_version TEXT NOT NULL DEFAULT '';
ALTER TABLE approval_records ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ;

CREATE UNIQUE INDEX IF NOT EXISTS execution_records_candidate_action_unique ON execution_records(candidate_action_id);
CREATE INDEX IF NOT EXISTS incidents_external_alert_lookup ON incidents(alert_source, external_alert_id, created_at DESC);
CREATE INDEX IF NOT EXISTS audit_events_incident_lookup ON audit_events(incident_id, started_at);
CREATE INDEX IF NOT EXISTS candidate_actions_incident_lookup ON candidate_actions(incident_id, created_at);
CREATE INDEX IF NOT EXISTS workflow_jobs_claim_lookup ON workflow_jobs(status, available_at, lease_until);
CREATE INDEX IF NOT EXISTS sessions_expiry_lookup ON sessions(expires_at);
