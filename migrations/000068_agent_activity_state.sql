-- +goose Up

-- One definition of an agent's activity state, shared by the agent list filter
-- and the activity reads. Declared as a set-returning SQL function so callers
-- can CROSS JOIN LATERAL it and Postgres inlines the body into the query.
-- +goose StatementBegin
CREATE FUNCTION agent_activity_state(agent_id uuid, agent_state text)
RETURNS TABLE (state text)
LANGUAGE sql
STABLE
PARALLEL SAFE
AS $$
    SELECT CASE
        WHEN agent_activity_state.agent_state = 'archived' THEN 'archived'
        WHEN EXISTS (
            SELECT 1
            FROM agent_interactions interaction
            WHERE interaction.agent_id = agent_activity_state.agent_id
              AND interaction.interaction_kind IN ('question', 'permission')
              AND interaction.state = 'open'
        ) THEN 'waiting_on_interaction'
        WHEN EXISTS (
            SELECT 1
            FROM agent_runtime_locks runtime_lock
            WHERE runtime_lock.agent_id = agent_activity_state.agent_id
        ) OR EXISTS (
            SELECT 1
            FROM agent_wakeups wake
            WHERE wake.agent_id = agent_activity_state.agent_id
        ) OR EXISTS (
            SELECT 1
            FROM agent_execution_state execution_state
            WHERE execution_state.agent_id = agent_activity_state.agent_id
              AND (execution_state.logical_ready_at IS NOT NULL OR execution_state.incomplete_tools)
        ) THEN 'running'
        ELSE 'idle'
    END;
$$;
-- +goose StatementEnd
