-- name: ExecutionSampleCandidates :many
SELECT id,project_id,root_agent_id
FROM agents
WHERE state='active' AND id>sqlc.arg(after_id)::uuid
ORDER BY id
LIMIT sqlc.arg(sample_size)::int;
