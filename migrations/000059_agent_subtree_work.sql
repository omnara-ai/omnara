-- +goose Up

-- Background processes an agent is waiting on: unfinished processes that will
-- notify it on exit, for at most an hour after they start.
-- +goose StatementBegin
CREATE FUNCTION agent_background_processes(project_id uuid, agent_id uuid, as_of timestamptz)
RETURNS TABLE (id uuid, created_at timestamptz)
LANGUAGE sql
STABLE
PARALLEL SAFE
AS $$
    SELECT process.id, process.created_at
    FROM processes process
    WHERE process.project_id = agent_background_processes.project_id
      AND process.agent_id = agent_background_processes.agent_id
      AND process.state IN ('queued', 'starting', 'running')
      AND process.notify_on_exit
      AND process.created_at > agent_background_processes.as_of - interval '1 hour';
$$;
-- +goose StatementEnd

-- Whether an agent or any active descendant is running, has a wakeup or
-- incomplete tools, or is waiting on a background process. A pending turn
-- always has a wakeup or runtime lock, so logical_ready_at is not checked.
-- +goose StatementBegin
CREATE FUNCTION agent_subtree_has_work(project_id uuid, agent_id uuid, as_of timestamptz)
RETURNS boolean
LANGUAGE sql
STABLE
PARALLEL SAFE
AS $$
    WITH RECURSIVE subtree AS (
        SELECT agent_subtree_has_work.agent_id AS id, 1 AS depth
        UNION ALL
        SELECT child.id, subtree.depth + 1
        FROM agents child
        JOIN subtree ON child.parent_agent_id = subtree.id
        WHERE child.project_id = agent_subtree_has_work.project_id
          AND child.state = 'active'
          AND subtree.depth < 64
    )
    SELECT EXISTS (
        SELECT 1
        FROM subtree
        WHERE EXISTS (SELECT 1 FROM agent_runtime_locks runtime_lock WHERE runtime_lock.agent_id = subtree.id)
           OR EXISTS (SELECT 1 FROM agent_wakeups wake WHERE wake.agent_id = subtree.id)
           OR EXISTS (SELECT 1 FROM agent_execution_state h WHERE h.agent_id = subtree.id AND h.incomplete_tools)
           OR EXISTS (
               SELECT 1
               FROM agent_background_processes(agent_subtree_has_work.project_id, subtree.id, agent_subtree_has_work.as_of)
           )
    );
$$;
-- +goose StatementEnd
