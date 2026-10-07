ALTER TABLE repository_bindings
    ADD COLUMN validation_profile_json JSONB,
    ADD COLUMN credential_ref TEXT NOT NULL DEFAULT '',
    ADD COLUMN automation_json JSONB;

-- One automatic run per incident episode/binding, including terminal failures.
-- Retained rows also consume the grant quota; restart does not replenish it.
CREATE TABLE repair_automatic_dispatches (
    incident_id TEXT NOT NULL REFERENCES incidents(id),
    binding_id TEXT NOT NULL REFERENCES repository_bindings(id),
    case_id TEXT NOT NULL UNIQUE REFERENCES repair_cases(id),
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (incident_id,binding_id)
);
