-- +goose Up

CREATE INDEX model_call_contexts_org_usage_created_idx
    ON model_call_contexts(org_id, created_at)
    WHERE input_tokens_total IS NOT NULL
       OR output_tokens_total IS NOT NULL
       OR provider_reported_cost_usd IS NOT NULL;

CREATE INDEX agent_inputs_content_project_queued_idx
    ON agent_inputs(project_id, queued_at)
    WHERE input_kind = 'content';
