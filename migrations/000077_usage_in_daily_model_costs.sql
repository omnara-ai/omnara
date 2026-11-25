-- +goose Up

ALTER TABLE daily_model_costs
    ADD COLUMN configured_model_revision_id uuid,
    ADD COLUMN model_calls bigint NOT NULL DEFAULT 0,
    ADD COLUMN model_calls_with_effective_cost bigint NOT NULL DEFAULT 0,
    ADD COLUMN model_calls_with_reported_cost bigint NOT NULL DEFAULT 0,
    ADD COLUMN provider_reported_cost_usd numeric NOT NULL DEFAULT 0,
    ADD COLUMN uncached_input_tokens bigint NOT NULL DEFAULT 0,
    ADD COLUMN cache_read_input_tokens bigint NOT NULL DEFAULT 0,
    ADD COLUMN cache_write_input_tokens bigint NOT NULL DEFAULT 0,
    ADD COLUMN output_tokens_total bigint NOT NULL DEFAULT 0,
    ADD COLUMN reasoning_output_tokens bigint NOT NULL DEFAULT 0,
    ADD CHECK (
        model_calls_with_effective_cost BETWEEN 0 AND model_calls
        AND model_calls_with_reported_cost BETWEEN 0 AND model_calls
        AND provider_reported_cost_usd >= 0
        AND least(uncached_input_tokens, cache_read_input_tokens, cache_write_input_tokens, output_tokens_total,
            reasoning_output_tokens) >= 0
    ),
    ADD FOREIGN KEY (org_id, configured_model_revision_id) REFERENCES configured_model_revisions(org_id, id),
    ADD CONSTRAINT daily_model_costs_agent_revision
        CHECK ((agent_id IS NULL) = (configured_model_revision_id IS NULL)) NOT VALID;

DROP INDEX daily_model_costs_scope_day_idx;

CREATE UNIQUE INDEX daily_model_costs_scope_day_idx
    ON daily_model_costs(org_id, project_id, agent_profile_id, agent_id, day, configured_model_revision_id)
    NULLS NOT DISTINCT;
