-- Null preserves the explicit historical queue contract and its scope digests.
-- New repository onboarding requires a versioned telemetry profile.
ALTER TABLE repository_bindings ADD COLUMN telemetry_profile_json JSONB;
ALTER TABLE repair_deployments ADD COLUMN telemetry_contract_json JSONB;
