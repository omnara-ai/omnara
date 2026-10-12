-- +goose NO TRANSACTION
-- +goose Up

CREATE INDEX CONCURRENTLY tool_calls_runtime_reference_idx
    ON tool_calls(runtime_lock_id) WHERE runtime_lock_id IS NOT NULL;

DROP INDEX CONCURRENTLY tool_calls_runtime_lock_idx;

CREATE INDEX CONCURRENTLY agent_inputs_received_content_idx
    ON agent_inputs(agent_id, input_rank, queued_at, id)
    WHERE state = 'received' AND input_kind = 'content';

CREATE UNIQUE INDEX CONCURRENTLY agents_org_project_id_key
    ON agents(org_id, project_id, id);

ALTER TABLE model_call_contexts ADD CONSTRAINT model_call_contexts_agent_scope_fkey
    FOREIGN KEY (org_id, project_id, agent_id) REFERENCES agents(org_id, project_id, id) NOT VALID;

ALTER TABLE processes ADD CONSTRAINT processes_agent_scope_fkey
    FOREIGN KEY (org_id, project_id, agent_id) REFERENCES agents(org_id, project_id, id) NOT VALID;

ALTER TABLE process_actions ADD CONSTRAINT process_actions_agent_scope_fkey
    FOREIGN KEY (org_id, project_id, agent_id) REFERENCES agents(org_id, project_id, id) NOT VALID;

ALTER TABLE model_call_contexts VALIDATE CONSTRAINT model_call_contexts_agent_scope_fkey;
ALTER TABLE processes VALIDATE CONSTRAINT processes_agent_scope_fkey;
ALTER TABLE process_actions VALIDATE CONSTRAINT process_actions_agent_scope_fkey;

ALTER TABLE model_call_contexts
    DROP CONSTRAINT model_call_contexts_org_id_fkey,
    DROP CONSTRAINT model_call_contexts_org_id_project_id_fkey,
    DROP CONSTRAINT model_call_contexts_project_id_agent_id_fkey;

ALTER TABLE agent_inputs DROP CONSTRAINT agent_inputs_project_id_fkey;

ALTER TABLE processes
    DROP CONSTRAINT processes_org_id_fkey,
    DROP CONSTRAINT processes_org_id_project_id_fkey,
    DROP CONSTRAINT processes_project_id_agent_id_fkey;

ALTER TABLE process_actions
    DROP CONSTRAINT process_actions_org_id_fkey,
    DROP CONSTRAINT process_actions_org_id_project_id_fkey;
