ALTER TABLE repair_approvals ADD COLUMN patch_sha256 TEXT NOT NULL DEFAULT '';

ALTER TABLE repair_publications ADD COLUMN patch_sha256 TEXT NOT NULL DEFAULT '';
ALTER TABLE repair_publications ADD COLUMN merge_sha TEXT NOT NULL DEFAULT '';
ALTER TABLE repair_publications ADD COLUMN approval_id TEXT REFERENCES repair_approvals(id);
ALTER TABLE repair_publications ADD COLUMN lease_owner TEXT NOT NULL DEFAULT '';
ALTER TABLE repair_publications ADD COLUMN lease_token TEXT NOT NULL DEFAULT '';
ALTER TABLE repair_publications ADD COLUMN lease_until TIMESTAMPTZ;
ALTER TABLE repair_publications ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE repair_publications ADD COLUMN failures INTEGER NOT NULL DEFAULT 0;
ALTER TABLE repair_publications ADD COLUMN available_at TIMESTAMPTZ NOT NULL DEFAULT now();
ALTER TABLE repair_publications ADD COLUMN last_error TEXT NOT NULL DEFAULT '';
CREATE INDEX repair_publications_claim_lookup ON repair_publications(state, lease_until, created_at);
CREATE UNIQUE INDEX repair_publications_branch_unique ON repair_publications(branch_name);

CREATE TABLE repair_github_deliveries (
    delivery_id TEXT PRIMARY KEY,
    publication_id TEXT NOT NULL REFERENCES repair_publications(id) ON DELETE RESTRICT,
    event_type TEXT NOT NULL,
    action TEXT NOT NULL,
    received_at TIMESTAMPTZ NOT NULL
);
