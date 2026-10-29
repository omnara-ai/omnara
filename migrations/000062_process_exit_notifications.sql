-- +goose Up

ALTER TABLE processes
    ADD COLUMN notify_on_exit boolean NOT NULL DEFAULT false,
    ADD COLUMN notify_match text NOT NULL DEFAULT '',
    ADD COLUMN exit_notice_decided boolean NOT NULL DEFAULT false;

-- Unfinished processes that will notify the agent on exit.
-- +goose StatementBegin
CREATE FUNCTION agent_background_processes(project_id uuid, agent_id uuid)
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
      AND process.notify_on_exit;
$$;
-- +goose StatementEnd

-- Whether an agent or any active descendant is running, has a wakeup or
-- incomplete tools, or is waiting on a background process. A pending turn
-- always has a wakeup or runtime lock, so logical_ready_at is not checked.
-- +goose StatementBegin
CREATE FUNCTION agent_subtree_has_work(project_id uuid, agent_id uuid)
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
               FROM agent_background_processes(agent_subtree_has_work.project_id, subtree.id)
           )
    );
$$;
-- +goose StatementEnd
