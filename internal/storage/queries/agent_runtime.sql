-- name: InsertAgent :one
-- @sqlc-vet-disable configured-models-deleted-at model-provider-configs-deleted-at
-- Display-only model names must still resolve after the model or provider config is soft deleted.
WITH inserted AS (
    INSERT INTO agents(
        org_id, project_id, state, name, agent_profile_id, current_config_id,
        idempotency_key, created_at, updated_at
    )
    VALUES (
        sqlc.arg(org_id), sqlc.arg(project_id), 'active', sqlc.arg(name),
        sqlc.narg(agent_profile_id), sqlc.arg(current_config_id), sqlc.narg(idempotency_key),
        transaction_timestamp(), transaction_timestamp()
    )
    ON CONFLICT (project_id, idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING
    RETURNING id, org_id, project_id, state, name,
              agent_profile_id, current_config_id, integration_target_id,
              idempotency_key, next_event_sequence, created_at, updated_at, archived_at
)
SELECT agent.id, agent.org_id, agent.project_id, agent.state, agent.name,
       agent.agent_profile_id, agent.current_config_id, agent.integration_target_id,
       coalesce(agent.idempotency_key, '') AS idempotency_key,
       agent.next_event_sequence, agent.created_at, agent.updated_at, agent.archived_at,
       coalesce(configured_model.name, '') AS model_name,
       coalesce(model_provider_config.name, '') AS model_provider_config_name
FROM inserted agent
LEFT JOIN agent_configs agent_config
  ON agent_config.project_id = agent.project_id
 AND agent_config.id = agent.current_config_id
LEFT JOIN configured_models configured_model
  ON configured_model.org_id = agent.org_id
 AND configured_model.id = agent_config.configured_model_id
LEFT JOIN model_provider_configs model_provider_config
  ON model_provider_config.org_id = configured_model.org_id
 AND model_provider_config.id = configured_model.model_provider_config_id;

-- name: LockProjectAgentLifecycleShared :exec
-- Agent creation takes the shared side: creates never conflict with each
-- other, only with project/org deletion, which holds the exclusive side.
SELECT pg_advisory_xact_lock_shared(hashtextextended(sqlc.arg(project_id)::text, 0));

-- name: LockProjectAgentLifecycleExclusive :exec
SELECT pg_advisory_xact_lock(hashtextextended(sqlc.arg(project_id)::text, 0));

-- name: LockAgentLaunchIdempotencyKey :exec
SELECT pg_advisory_xact_lock(
  hashtextextended(
    sqlc.arg(project_id)::uuid::text,
    hashtextextended(sqlc.arg(idempotency_key)::text, 0)
  )
);

-- name: GetAgentByIdempotencyKey :one
-- @sqlc-vet-disable configured-models-deleted-at model-provider-configs-deleted-at
-- Display-only model names must still resolve after the model or provider config is soft deleted.
SELECT agent.id, agent.org_id, agent.project_id, agent.state, agent.name,
       agent.agent_profile_id, agent.current_config_id, agent.integration_target_id,
       coalesce(agent.idempotency_key, '') AS idempotency_key,
       agent.next_event_sequence, agent.created_at, agent.updated_at, agent.archived_at,
       coalesce(configured_model.name, '') AS model_name,
       coalesce(model_provider_config.name, '') AS model_provider_config_name
FROM agents agent
LEFT JOIN agent_configs agent_config
  ON agent_config.project_id = agent.project_id
 AND agent_config.id = agent.current_config_id
LEFT JOIN configured_models configured_model
  ON configured_model.org_id = agent.org_id
 AND configured_model.id = agent_config.configured_model_id
LEFT JOIN model_provider_configs model_provider_config
  ON model_provider_config.org_id = configured_model.org_id
 AND model_provider_config.id = configured_model.model_provider_config_id
WHERE agent.project_id = sqlc.arg(project_id)
  AND agent.idempotency_key = sqlc.arg(idempotency_key)::text;

-- name: GetAgent :one
SELECT id, org_id, project_id, state, name,
       agent_profile_id, current_config_id, integration_target_id,
       coalesce(idempotency_key, '') AS idempotency_key,
       next_event_sequence, created_at, updated_at, archived_at
FROM agents
WHERE id = $1;

-- name: GetAgentInProject :one
-- @sqlc-vet-disable configured-models-deleted-at model-provider-configs-deleted-at
-- Display-only model names must still resolve after the model or provider config is soft deleted.
SELECT agent.id, agent.org_id, agent.project_id, agent.state, agent.name,
       agent.agent_profile_id, agent.current_config_id, agent.integration_target_id,
       coalesce(agent.idempotency_key, '') AS idempotency_key,
       agent.next_event_sequence, agent.created_at, agent.updated_at, agent.archived_at,
       coalesce(configured_model.name, '') AS model_name,
       coalesce(model_provider_config.name, '') AS model_provider_config_name
FROM agents agent
LEFT JOIN agent_configs agent_config
  ON agent_config.project_id = agent.project_id
 AND agent_config.id = agent.current_config_id
LEFT JOIN configured_models configured_model
  ON configured_model.org_id = agent.org_id
 AND configured_model.id = agent_config.configured_model_id
LEFT JOIN model_provider_configs model_provider_config
  ON model_provider_config.org_id = configured_model.org_id
 AND model_provider_config.id = configured_model.model_provider_config_id
WHERE agent.project_id = $1 AND agent.id = $2;

-- name: ListAgentsForProject :many
WITH listed AS (
SELECT agent.id,
       agent.org_id,
       agent.project_id,
       agent.state,
       agent.name,
       agent.agent_profile_id,
       agent.current_config_id,
       agent.integration_target_id,
       coalesce(agent.idempotency_key, '') AS idempotency_key,
       agent.next_event_sequence,
       agent.created_at,
       agent.updated_at,
       agent.archived_at,
       coalesce(install.provider, '') AS integration_target_provider,
       coalesce(install.provider_tenant_id, '') AS integration_target_provider_tenant_id,
       coalesce(target.provider_ref, '') AS integration_target_provider_ref,
       coalesce(target.provider_ref_kind, '') AS integration_target_provider_ref_kind,
       coalesce(target.display_name, '') AS integration_target_display_name,
       configured_model.name AS model_name,
       model_provider_config.name AS model_provider_config_name,
       CASE sqlc.arg(sort_field)::text
         WHEN 'name' THEN lower(agent.name)
         WHEN 'created_at' THEN to_char(agent.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US')
         WHEN 'updated_at' THEN to_char(agent.updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US')
         WHEN 'state' THEN agent.state
         WHEN 'integration_provider' THEN lower(install.provider)
         WHEN 'integration_target_kind' THEN lower(target.provider_ref_kind)
       END::text AS sort_key,
       CASE sqlc.arg(sort_field)::text
         WHEN 'integration_provider' THEN target.id IS NULL
         WHEN 'integration_target_kind' THEN target.id IS NULL
         ELSE false
       END AS sort_is_null
FROM agents agent
LEFT JOIN integration_targets target
  ON target.project_id = agent.project_id
 AND target.agent_id = agent.id
 AND target.id = agent.integration_target_id
 AND target.deleted_at IS NULL
LEFT JOIN integration_installs install
  ON install.project_id = target.project_id
 AND install.id = target.integration_install_id
 AND install.deleted_at IS NULL
JOIN agent_configs agent_config
  ON agent_config.project_id = agent.project_id
 AND agent_config.id = agent.current_config_id
JOIN configured_models configured_model
  ON configured_model.org_id = agent.org_id
 AND configured_model.id = agent_config.configured_model_id
JOIN model_provider_configs model_provider_config
  ON model_provider_config.org_id = configured_model.org_id
 AND model_provider_config.id = configured_model.model_provider_config_id
WHERE agent.project_id = sqlc.arg(project_id)
  AND agent.state = 'active'
  AND (sqlc.arg(name_pattern)::text = '' OR agent.name ILIKE sqlc.arg(name_pattern)::text ESCAPE '\')
  AND (COALESCE(cardinality(sqlc.arg(integration_providers)::text[]), 0) = 0 OR install.provider = ANY(sqlc.arg(integration_providers)::text[]))
  AND (COALESCE(cardinality(sqlc.arg(integration_target_kinds)::text[]), 0) = 0 OR target.provider_ref_kind = ANY(sqlc.arg(integration_target_kinds)::text[]))
  AND (sqlc.narg(has_integration_target)::boolean IS NULL OR (target.id IS NOT NULL) = sqlc.narg(has_integration_target)::boolean)
  AND (sqlc.narg(agent_profile_id)::uuid IS NULL OR agent.agent_profile_id = sqlc.narg(agent_profile_id)::uuid)
)
SELECT id, org_id, project_id, state, name, agent_profile_id, current_config_id,
       integration_target_id, idempotency_key,
       next_event_sequence, created_at, updated_at,
       archived_at, integration_target_provider,
       integration_target_provider_tenant_id, integration_target_provider_ref,
       integration_target_provider_ref_kind,
       integration_target_display_name, model_name,
       model_provider_config_name, sort_key, sort_is_null
FROM listed
WHERE sqlc.arg(cursor_set)::boolean = false
   OR sort_is_null > sqlc.arg(cursor_is_null)::boolean
   OR (sort_is_null = sqlc.arg(cursor_is_null)::boolean AND (
        (sqlc.arg(sort_desc)::boolean = false AND (sort_key, id) > (sqlc.arg(cursor_key)::text, sqlc.arg(cursor_id)::uuid))
     OR (sqlc.arg(sort_desc)::boolean = true AND (sort_key, id) < (sqlc.arg(cursor_key)::text, sqlc.arg(cursor_id)::uuid))
   ))
ORDER BY sort_is_null ASC,
         CASE WHEN sqlc.arg(sort_desc)::boolean = false THEN sort_key END ASC,
         CASE WHEN sqlc.arg(sort_desc)::boolean = true THEN sort_key END DESC,
         CASE WHEN sqlc.arg(sort_desc)::boolean = false THEN id END ASC,
         CASE WHEN sqlc.arg(sort_desc)::boolean = true THEN id END DESC
LIMIT sqlc.arg(row_limit)::bigint;

-- name: ListAgentsForProjectByCreatedAtDesc :many
SELECT agent.id,
       agent.org_id,
       agent.project_id,
       agent.state,
       agent.name,
       agent.agent_profile_id,
       agent.current_config_id,
       agent.integration_target_id,
       coalesce(agent.idempotency_key, '') AS idempotency_key,
       agent.next_event_sequence,
       agent.created_at,
       agent.updated_at,
       agent.archived_at,
       coalesce(install.provider, '') AS integration_target_provider,
       coalesce(install.provider_tenant_id, '') AS integration_target_provider_tenant_id,
       coalesce(target.provider_ref, '') AS integration_target_provider_ref,
       coalesce(target.provider_ref_kind, '') AS integration_target_provider_ref_kind,
       coalesce(target.display_name, '') AS integration_target_display_name,
       configured_model.name AS model_name,
       model_provider_config.name AS model_provider_config_name
FROM agents agent
LEFT JOIN integration_targets target
  ON target.project_id = agent.project_id
 AND target.agent_id = agent.id
 AND target.id = agent.integration_target_id
 AND target.deleted_at IS NULL
LEFT JOIN integration_installs install
  ON install.project_id = target.project_id
 AND install.id = target.integration_install_id
 AND install.deleted_at IS NULL
JOIN agent_configs agent_config
  ON agent_config.project_id = agent.project_id
 AND agent_config.id = agent.current_config_id
JOIN configured_models configured_model
  ON configured_model.org_id = agent.org_id
 AND configured_model.id = agent_config.configured_model_id
JOIN model_provider_configs model_provider_config
  ON model_provider_config.org_id = configured_model.org_id
 AND model_provider_config.id = configured_model.model_provider_config_id
WHERE agent.project_id = sqlc.arg(project_id)
  AND agent.state = 'active'
  AND (sqlc.arg(name_pattern)::text = '' OR agent.name ILIKE sqlc.arg(name_pattern)::text ESCAPE '\')
  AND (COALESCE(cardinality(sqlc.arg(integration_providers)::text[]), 0) = 0 OR install.provider = ANY(sqlc.arg(integration_providers)::text[]))
  AND (COALESCE(cardinality(sqlc.arg(integration_target_kinds)::text[]), 0) = 0 OR target.provider_ref_kind = ANY(sqlc.arg(integration_target_kinds)::text[]))
  AND (sqlc.narg(has_integration_target)::boolean IS NULL OR (target.id IS NOT NULL) = sqlc.narg(has_integration_target)::boolean)
  AND (sqlc.narg(agent_profile_id)::uuid IS NULL OR agent.agent_profile_id = sqlc.narg(agent_profile_id)::uuid)
  AND (
    sqlc.arg(cursor_set)::boolean = false
    OR (agent.created_at, agent.id) < (sqlc.arg(cursor_created_at)::timestamptz, sqlc.arg(cursor_id)::uuid)
  )
ORDER BY agent.created_at DESC, agent.id DESC
LIMIT sqlc.arg(row_limit)::bigint;

-- name: ListRecentAgentsForProjects :many
SELECT agent.id,
       agent.org_id,
       agent.project_id,
       agent.state,
       agent.name,
       agent.agent_profile_id,
       agent.current_config_id,
       agent.integration_target_id,
       coalesce(agent.idempotency_key, '') AS idempotency_key,
       agent.next_event_sequence,
       agent.created_at,
       agent.updated_at,
       agent.archived_at,
       coalesce(install.provider, '') AS integration_target_provider,
       coalesce(install.provider_tenant_id, '') AS integration_target_provider_tenant_id,
       coalesce(target.provider_ref, '') AS integration_target_provider_ref,
       coalesce(target.provider_ref_kind, '') AS integration_target_provider_ref_kind,
       coalesce(target.display_name, '') AS integration_target_display_name,
       configured_model.name AS model_name,
       model_provider_config.name AS model_provider_config_name
FROM agents agent
LEFT JOIN integration_targets target
  ON target.project_id = agent.project_id
 AND target.agent_id = agent.id
 AND target.id = agent.integration_target_id
 AND target.deleted_at IS NULL
LEFT JOIN integration_installs install
  ON install.project_id = target.project_id
 AND install.id = target.integration_install_id
 AND install.deleted_at IS NULL
JOIN agent_configs agent_config
  ON agent_config.project_id = agent.project_id
 AND agent_config.id = agent.current_config_id
JOIN configured_models configured_model
  ON configured_model.org_id = agent.org_id
 AND configured_model.id = agent_config.configured_model_id
JOIN model_provider_configs model_provider_config
  ON model_provider_config.org_id = configured_model.org_id
 AND model_provider_config.id = configured_model.model_provider_config_id
WHERE agent.project_id = ANY(sqlc.arg(project_ids)::uuid[])
  AND agent.state = 'active'
ORDER BY agent.updated_at DESC, agent.id DESC
LIMIT sqlc.arg(row_limit)::bigint;

-- name: ArchiveAgent :execrows
-- Idempotent: archiving an already-archived agent matches the row and keeps
-- the original archived_at.
UPDATE agents
SET state = 'archived',
    archived_at = coalesce(archived_at, statement_timestamp()),
    updated_at = CASE
      WHEN state = 'archived' THEN updated_at
      ELSE statement_timestamp()
    END
WHERE project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(id);

-- name: AgentExistsInProject :one
SELECT EXISTS (
    SELECT 1
    FROM agents
    WHERE project_id = $1 AND id = $2
);

-- name: LockAgentInProject :one
SELECT id, org_id
FROM agents
WHERE project_id = $1 AND id = $2
FOR UPDATE;
