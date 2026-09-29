ALTER TABLE repair_deployments ADD COLUMN phase TEXT NOT NULL DEFAULT 'started';
ALTER TABLE repair_deployments ADD COLUMN baseline_json JSONB;
ALTER TABLE repair_deployments ADD COLUMN completed_at TIMESTAMPTZ;
ALTER TABLE repair_deployments ADD COLUMN lease_token TEXT NOT NULL DEFAULT '';
ALTER TABLE repair_deployments ADD COLUMN lease_until TIMESTAMPTZ;
ALTER TABLE repair_deployments ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE repair_deployments ADD COLUMN result_json JSONB;
CREATE INDEX repair_deployments_pending_lookup ON repair_deployments(verification_status, lease_until, completed_at);

CREATE TABLE repair_verification_samples (
    deployment_id TEXT NOT NULL REFERENCES repair_deployments(deployment_id) ON DELETE RESTRICT,
    observation_number INTEGER NOT NULL CHECK (observation_number > 0),
    captured_at TIMESTAMPTZ NOT NULL,
    sample_json JSONB NOT NULL,
    passed BOOLEAN NOT NULL,
    PRIMARY KEY (deployment_id, observation_number)
);
