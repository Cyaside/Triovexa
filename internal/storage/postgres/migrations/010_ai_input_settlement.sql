-- Upgrade with ledger writers stopped. Original request bounds remain immutable;
-- a settled request contributes its validated prompt usage to the campaign cap.
LOCK TABLE ai_budget_campaigns, ai_model_requests IN ACCESS EXCLUSIVE MODE;

DO $$
BEGIN
    -- An accounted row must carry a valid receipt before it can release input.
    -- Reject corrupt accounting rather than interpreting missing values as zero.
    IF EXISTS (
        SELECT 1 FROM ai_model_requests r
        WHERE r.state = 'accounted' AND (
            CASE WHEN
                r.receipt_json ->> 'request_id' = r.id
                AND r.receipt_json ->> 'payload_hash' = r.payload_hash
                AND r.receipt_json #> '{usage,present}' = 'true'::jsonb
                AND r.identity_json ->> 'input_token_bound' = r.input_token_bound::text
                AND r.identity_json ->> 'output_token_bound' ~ '^[1-9][0-9]*$'
                AND NOT EXISTS (
                    SELECT 1
                    FROM (VALUES ('prompt_tokens'), ('completion_tokens'), ('total_tokens'),
                        ('cached_input_tokens'), ('reasoning_tokens')) AS field(name)
                    WHERE jsonb_typeof(r.receipt_json #> ARRAY['usage', field.name]) IS DISTINCT FROM 'number'
                        OR (r.receipt_json #>> ARRAY['usage', field.name]) !~ '^(0|[1-9][0-9]*)$'
                )
            THEN
                (r.identity_json ->> 'output_token_bound')::numeric <= 9223372036854775807
                AND (r.receipt_json #>> '{usage,total_tokens}')::numeric <= 9223372036854775807
                AND (r.receipt_json #>> '{usage,prompt_tokens}')::numeric <= r.input_token_bound
                AND (r.receipt_json #>> '{usage,completion_tokens}')::numeric <= (r.identity_json ->> 'output_token_bound')::numeric
                AND (r.receipt_json #>> '{usage,total_tokens}')::numeric =
                    (r.receipt_json #>> '{usage,prompt_tokens}')::numeric + (r.receipt_json #>> '{usage,completion_tokens}')::numeric
                AND (r.receipt_json #>> '{usage,cached_input_tokens}')::numeric <= (r.receipt_json #>> '{usage,prompt_tokens}')::numeric
                AND (r.receipt_json #>> '{usage,reasoning_tokens}')::numeric <= (r.receipt_json #>> '{usage,completion_tokens}')::numeric
            ELSE false END
        ) IS NOT TRUE
    ) THEN
        RAISE EXCEPTION 'input settlement requires valid accounted model receipts';
    END IF;

    -- Allow either the old bound total or the new settled total for idempotency.
    -- Neither an inconsistent counter nor an orphaned allowance is reset.
    IF EXISTS (
        SELECT 1 FROM ai_budget_campaigns c
        LEFT JOIN LATERAL (
            SELECT COALESCE(SUM(CASE WHEN r.state = 'cancelled' THEN 0 ELSE r.input_token_bound END), 0) AS bound_total,
                COALESCE(SUM(CASE WHEN r.state = 'cancelled' THEN 0
                    WHEN r.state = 'accounted' THEN (r.receipt_json #>> '{usage,prompt_tokens}')::bigint
                    ELSE r.input_token_bound END), 0) AS settled_total
            FROM ai_model_requests r WHERE r.campaign_id = c.id
        ) totals ON true
        WHERE c.admitted_input_tokens <> totals.bound_total
            AND c.admitted_input_tokens <> totals.settled_total
    ) THEN
        RAISE EXCEPTION 'input settlement found inconsistent campaign accounting';
    END IF;
END $$;

UPDATE ai_budget_campaigns c SET admitted_input_tokens = totals.input_total
FROM (
    SELECT campaign_id, SUM(CASE WHEN state = 'cancelled' THEN 0
        WHEN state = 'accounted' THEN (receipt_json #>> '{usage,prompt_tokens}')::bigint
        ELSE input_token_bound END) AS input_total
    FROM ai_model_requests GROUP BY campaign_id
) totals WHERE c.id = totals.campaign_id;
