-- +goose NO TRANSACTION
-- +goose Up

DROP INDEX CONCURRENTLY IF EXISTS model_call_contexts_usage_recovery_idx;

CREATE INDEX CONCURRENTLY model_call_contexts_usage_recovery_idx
    ON model_call_contexts(completed_at, id)
    WHERE state IN ('failed', 'canceled')
      AND api_variant = 'openrouter'
      AND provider_response_id <> ''
      AND provider_reported_cost_usd IS NULL
      AND input_tokens_total IS NULL
      AND output_tokens_total IS NULL;
