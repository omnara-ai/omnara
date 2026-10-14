-- name: LoadExecutionScope :one
SELECT a.root_agent_id FROM agents a WHERE a.project_id=$1 AND a.id=$2;

-- name: LoadExecutionFamily :one
SELECT root_agent_id,parent_agent_id FROM agents WHERE project_id=$1 AND id=$2;
