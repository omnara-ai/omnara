-- name: CreateExecutionAgent :one
INSERT INTO agents(id,org_id,project_id,root_agent_id,parent_agent_id,subagent_key,state,name,agent_profile_id,
 current_config_id,idempotency_key,archive_after_idle_minutes,created_at,updated_at)
 SELECT sqlc.arg(id),p.org_id,p.id,sqlc.arg(root_id),sqlc.narg(parent_id),sqlc.arg(subagent_key),'active',sqlc.arg(name),
 sqlc.narg(profile_id),sqlc.arg(config_id),sqlc.narg(idempotency_key),sqlc.narg(archive_after_idle_minutes),
 statement_timestamp(),statement_timestamp()
 FROM projects p JOIN orgs o ON o.id=p.org_id WHERE p.id=sqlc.arg(project_id) AND p.deleted_at IS NULL AND o.deleted_at IS NULL
 RETURNING id;

-- name: ReadExecutionAgentIdentity :one
SELECT org_id,parent_agent_id,root_agent_id,state,name,subagent_key FROM agents
WHERE project_id=sqlc.arg(project_id) AND id=sqlc.arg(id);

-- name: ArchiveExecutionAgent :execrows
UPDATE agents SET state='archived',archived_at=coalesce(archived_at,statement_timestamp()),updated_at=statement_timestamp()
WHERE id=sqlc.arg(id) AND state='active';

-- name: WriteExecutionInteractionTarget :execrows
UPDATE agents a SET interaction_target_id=sqlc.narg(target_id),interaction_handler_key=sqlc.narg(handler_key),
 interaction_auto_select=sqlc.arg(auto_select),updated_at=statement_timestamp()
WHERE a.id=sqlc.arg(id) AND ((sqlc.narg(target_id)::uuid IS NULL AND sqlc.narg(handler_key)::text IS NULL)
 OR (sqlc.narg(handler_key)::text<>'' AND EXISTS(SELECT 1 FROM integration_targets t JOIN integrations i
 ON i.project_id=t.project_id AND i.id=t.integration_id WHERE t.project_id=a.project_id AND t.agent_id=a.id
 AND t.id=sqlc.narg(target_id) AND t.deleted_at IS NULL AND i.deleted_at IS NULL AND i.state='active')));

-- name: ListExecutionIntegrationTargetAgents :many
-- @sqlc-vet-disable integration-targets-deleted-at
SELECT a.id FROM agents a JOIN integration_targets t ON t.project_id=a.project_id AND t.agent_id=a.id AND t.id=a.interaction_target_id
WHERE a.project_id=sqlc.arg(project_id) AND t.integration_id=sqlc.arg(integration_id) ORDER BY a.id;

-- name: ClearExecutionIntegrationTargets :exec
-- @sqlc-vet-disable integration-targets-deleted-at
UPDATE agents a SET interaction_target_id=NULL,interaction_handler_key=NULL,updated_at=statement_timestamp()
FROM integration_targets t WHERE a.project_id=sqlc.arg(project_id) AND a.id=ANY(sqlc.arg(agent_ids)::uuid[])
 AND t.project_id=a.project_id AND t.agent_id=a.id AND t.id=a.interaction_target_id AND t.integration_id=sqlc.arg(integration_id);

