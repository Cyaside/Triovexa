-- Runtime snapshots are inserted with approval/attempt/job, never retrofitted
-- onto an already-approved legacy attempt.
ALTER TABLE repair_attempts ADD COLUMN runtime_json JSONB;

CREATE TABLE repair_tool_receipts (
    attempt_id TEXT NOT NULL REFERENCES repair_attempts(id),
    call_id TEXT NOT NULL,
    name TEXT NOT NULL,
    args_sha256 TEXT NOT NULL,
    revision TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('started', 'completed')),
    result_sealed TEXT,
    created_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ,
    PRIMARY KEY (attempt_id, call_id),
    CHECK (length(args_sha256) = 64),
    CHECK ((state = 'started' AND result_sealed IS NULL) OR
           (state = 'completed' AND result_sealed LIKE 'v1.%'))
);
