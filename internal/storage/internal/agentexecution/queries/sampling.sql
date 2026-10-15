-- name: ExecutionSampleBounds :one
SELECT (SELECT id FROM agents ORDER BY id LIMIT 1)::uuid AS first_id,
       (SELECT id FROM agents ORDER BY id DESC LIMIT 1)::uuid AS last_id;

-- name: ExecutionSampleCandidates :many
WITH candidates AS MATERIALIZED (
    SELECT id,project_id,root_agent_id,state FROM agents
    WHERE id>=sqlc.arg(pivot)::uuid
    ORDER BY id LIMIT sqlc.arg(sample_size)::int
)
SELECT c.id,c.project_id,c.root_agent_id,
       (c.state='active' AND (SELECT h.agent_id FROM agent_execution_state h
        WHERE h.agent_id=c.id LIMIT 1) IS NOT NULL)::boolean AS eligible
FROM candidates c;
