-- +goose Up

ALTER TABLE model_call_contexts
    ADD COLUMN provider_reported_cost_usd numeric(30, 15);

ALTER TABLE model_call_contexts
    ADD CONSTRAINT model_call_contexts_provider_reported_cost_usd_check
    CHECK (
        provider_reported_cost_usd IS NULL
        OR (
            provider_reported_cost_usd <> 'NaN'::numeric
            AND
            provider_reported_cost_usd >= 0
            AND api_format <> ''
        )
    );
