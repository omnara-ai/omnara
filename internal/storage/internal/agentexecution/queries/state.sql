-- name: LoadExecutionScope :one
SELECT a.root_agent_id FROM agents a WHERE a.project_id=sqlc.arg(project_id) AND a.id=sqlc.arg(id);

-- name: LoadExecutionFamily :one
SELECT root_agent_id,parent_agent_id FROM agents WHERE project_id=sqlc.arg(project_id) AND id=sqlc.arg(id);
