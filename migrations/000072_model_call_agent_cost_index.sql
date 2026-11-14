-- +goose NO TRANSACTION
-- +goose Up

DROP INDEX CONCURRENTLY IF EXISTS model_call_contexts_agent_cost_idx;

CREATE INDEX CONCURRENTLY model_call_contexts_agent_cost_idx
    ON model_call_contexts(project_id, agent_id, created_at)
    INCLUDE (effective_cost_usd)
    WHERE effective_cost_usd IS NOT NULL;
