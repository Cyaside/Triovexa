-- Existing campaigns remain capped; no counters or receipts are reset.
ALTER TABLE ai_budget_campaigns ADD COLUMN provider_managed BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE ai_budget_campaigns
    DROP CONSTRAINT ai_budget_campaigns_max_input_tokens_check,
    DROP CONSTRAINT ai_budget_campaigns_max_requests_check,
    DROP CONSTRAINT ai_budget_campaigns_check,
    DROP CONSTRAINT ai_budget_campaigns_check1,
    DROP CONSTRAINT ai_budget_campaigns_check2,
    ADD CONSTRAINT ai_budget_campaign_limits CHECK (
        (provider_managed AND profile = 'internal' AND NOT offline
            AND max_spend_micro_usd = 0 AND max_input_tokens = 0 AND max_requests = 0)
        OR (NOT provider_managed AND max_input_tokens > 0 AND max_requests > 0
            AND spent_micro_usd <= max_spend_micro_usd - reserved_micro_usd
            AND admitted_input_tokens <= max_input_tokens AND requests <= max_requests)
    );
