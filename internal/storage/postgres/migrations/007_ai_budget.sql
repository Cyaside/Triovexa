CREATE TABLE ai_budget_campaigns (
    id TEXT PRIMARY KEY,
    profile TEXT NOT NULL,
    offline BOOLEAN NOT NULL,
    max_spend_micro_usd BIGINT NOT NULL CHECK (max_spend_micro_usd >= 0),
    max_input_tokens BIGINT NOT NULL CHECK (max_input_tokens > 0),
    max_requests BIGINT NOT NULL CHECK (max_requests > 0),
    spent_micro_usd BIGINT NOT NULL DEFAULT 0 CHECK (spent_micro_usd >= 0),
    reserved_micro_usd BIGINT NOT NULL DEFAULT 0 CHECK (reserved_micro_usd >= 0),
    admitted_input_tokens BIGINT NOT NULL DEFAULT 0 CHECK (admitted_input_tokens >= 0),
    requests BIGINT NOT NULL DEFAULT 0 CHECK (requests >= 0),
    blocked BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL,
    CHECK (spent_micro_usd <= max_spend_micro_usd - reserved_micro_usd),
    CHECK (admitted_input_tokens <= max_input_tokens),
    CHECK (requests <= max_requests)
);

CREATE TABLE ai_model_requests (
    id TEXT PRIMARY KEY,
    campaign_id TEXT NOT NULL REFERENCES ai_budget_campaigns(id) ON DELETE RESTRICT,
    identity_json JSONB NOT NULL,
    pricing_json JSONB NOT NULL,
    payload_hash TEXT NOT NULL CHECK (payload_hash ~ '^[0-9a-f]{64}$'),
    external BOOLEAN NOT NULL,
    input_token_bound BIGINT NOT NULL CHECK (input_token_bound > 0),
    reserved_micro_usd BIGINT NOT NULL CHECK (reserved_micro_usd >= 0),
    actual_micro_usd BIGINT NOT NULL DEFAULT 0 CHECK (actual_micro_usd >= 0),
    state TEXT NOT NULL CHECK (state IN ('reserved','dispatching','accounted','uncertain','cancelled')),
    receipt_json JSONB,
    response BYTEA,
    response_sha256 TEXT,
    created_at TIMESTAMPTZ NOT NULL,
    dispatched_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    CHECK (response IS NULL OR octet_length(response) <= 262144),
    CHECK (actual_micro_usd <= reserved_micro_usd)
);
CREATE INDEX ai_model_requests_campaign_state ON ai_model_requests(campaign_id,state);
