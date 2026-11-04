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

-- The per-agent activity details every activity read returns next to the
-- state. List pages join it in the same statement that filters them, so the
-- details and the state come from one snapshot.
-- +goose StatementBegin
CREATE FUNCTION agent_activity_details(project_id uuid, agent_id uuid, agent_created_at timestamptz)
RETURNS TABLE (
    last_activity_at timestamptz,
    open_question_count integer,
    open_permission_count integer,
    background_process_ids uuid[],
    working_subagent_ids uuid[]
)
LANGUAGE sql
STABLE
PARALLEL SAFE
AS $$
    SELECT
        coalesce((
            SELECT event.created_at
            FROM agent_events event
            WHERE event.agent_id = agent_activity_details.agent_id
            ORDER BY event.sequence DESC
            LIMIT 1
        ), agent_activity_details.agent_created_at),
        (
            SELECT count(*)
            FROM agent_interactions interaction
            WHERE interaction.agent_id = agent_activity_details.agent_id
              AND interaction.interaction_kind = 'question'
              AND interaction.state = 'open'
        )::integer,
        (
            SELECT count(*)
            FROM agent_interactions interaction
            WHERE interaction.agent_id = agent_activity_details.agent_id
              AND interaction.interaction_kind = 'permission'
              AND interaction.state = 'open'
        )::integer,
        ARRAY(
            SELECT process.id
            FROM agent_background_processes(agent_activity_details.project_id, agent_activity_details.agent_id) process
            ORDER BY process.created_at, process.id
        )::uuid[],
        ARRAY(
            SELECT child.id
            FROM agents child
            WHERE child.project_id = agent_activity_details.project_id
              AND child.parent_agent_id = agent_activity_details.agent_id
              AND child.state = 'active'
              AND agent_subtree_has_work(child.project_id, child.id)
            ORDER BY child.created_at, child.id
        )::uuid[];
$$;
-- +goose StatementEnd
