-- +goose NO TRANSACTION
-- +goose Up

DROP INDEX CONCURRENTLY IF EXISTS model_call_contexts_org_usage_created_idx;

CREATE INDEX CONCURRENTLY model_call_contexts_org_usage_created_idx
    ON model_call_contexts(org_id, created_at)
    WHERE input_tokens_total IS NOT NULL
       OR output_tokens_total IS NOT NULL
       OR provider_reported_cost_usd IS NOT NULL;

DROP INDEX CONCURRENTLY IF EXISTS agent_inputs_content_project_queued_idx;

CREATE INDEX CONCURRENTLY agent_inputs_content_project_queued_idx
    ON agent_inputs(project_id, queued_at)
    WHERE input_kind = 'content';

DROP INDEX CONCURRENTLY IF EXISTS agents_root_project_profile_idx;

CREATE INDEX CONCURRENTLY agents_root_project_profile_idx
    ON agents(project_id, agent_profile_id)
    WHERE parent_agent_id IS NULL;

DROP INDEX CONCURRENTLY IF EXISTS agents_root_project_created_idx;

CREATE INDEX CONCURRENTLY agents_root_project_created_idx
    ON agents(project_id, created_at)
    WHERE parent_agent_id IS NULL;
