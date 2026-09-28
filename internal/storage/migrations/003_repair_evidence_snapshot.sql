ALTER TABLE repair_evidence ADD COLUMN snapshot_json JSONB;

CREATE UNIQUE INDEX repair_evidence_one_snapshot_per_case
    ON repair_evidence(case_id) WHERE source = 'incident_snapshot';
