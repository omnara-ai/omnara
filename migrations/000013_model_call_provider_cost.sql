-- +goose Up

ALTER TABLE model_call_contexts
    ADD COLUMN provider_reported_cost_usd numeric(30, 15);

ALTER TABLE model_call_contexts
    ADD CONSTRAINT model_call_contexts_cost_valid
    CHECK (
        provider_reported_cost_usd IS NULL
        OR (
            provider_reported_cost_usd <> 'NaN'::numeric
            AND
            provider_reported_cost_usd >= 0
        )
    );

ALTER TABLE model_call_contexts
    ADD CONSTRAINT model_call_contexts_cost_has_api
    CHECK (
        provider_reported_cost_usd IS NULL
        OR api_format <> ''
    );

-- Supports bounded global keyset scans over provider-reported cost facts.
CREATE INDEX model_call_contexts_provider_cost_idx
    ON model_call_contexts(completed_at, id)
    WHERE provider_reported_cost_usd IS NOT NULL;
