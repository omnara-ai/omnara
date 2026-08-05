-- name: UpsertActorIdentity :one
INSERT INTO actors(project_id, provider, provider_tenant_id, provider_user_id, display_name, created_at, updated_at)
VALUES (
  sqlc.arg(project_id), sqlc.arg(provider), sqlc.narg(provider_tenant_id),
  sqlc.arg(provider_user_id), NULLIF(sqlc.arg(display_name)::text, ''),
  transaction_timestamp(), transaction_timestamp()
)
ON CONFLICT (project_id, provider, provider_tenant_id, provider_user_id) DO UPDATE
SET display_name = coalesce(excluded.display_name, actors.display_name),
    updated_at = excluded.updated_at
WHERE coalesce(excluded.display_name, actors.display_name) IS DISTINCT FROM actors.display_name
RETURNING id, project_id, provider, provider_tenant_id, provider_user_id, display_name, metadata, created_at, updated_at;

-- name: PutActor :one
INSERT INTO actors(project_id, provider, provider_tenant_id, provider_user_id, display_name, metadata, created_at, updated_at)
VALUES (
  sqlc.arg(project_id), 'external', sqlc.narg(provider_tenant_id),
  sqlc.arg(provider_user_id), NULLIF(sqlc.arg(display_name)::text, ''),
  sqlc.arg(metadata), transaction_timestamp(), transaction_timestamp()
)
ON CONFLICT (project_id, provider, provider_tenant_id, provider_user_id) DO UPDATE
SET display_name = CASE WHEN sqlc.arg(display_name_set)::boolean THEN excluded.display_name ELSE actors.display_name END,
    metadata = CASE WHEN sqlc.arg(metadata_set)::boolean THEN excluded.metadata ELSE actors.metadata END,
    updated_at = excluded.updated_at
WHERE (sqlc.arg(display_name_set)::boolean AND excluded.display_name IS DISTINCT FROM actors.display_name)
   OR (sqlc.arg(metadata_set)::boolean AND excluded.metadata IS DISTINCT FROM actors.metadata)
RETURNING id, project_id, provider, provider_tenant_id, provider_user_id, display_name, metadata, created_at, updated_at;

-- name: GetActorByIdentity :one
SELECT id, project_id, provider, provider_tenant_id, provider_user_id, display_name, metadata, created_at, updated_at
FROM actors
WHERE project_id = sqlc.arg(project_id)
  AND provider = sqlc.arg(provider)
  AND provider_tenant_id IS NOT DISTINCT FROM sqlc.narg(provider_tenant_id)
  AND provider_user_id = sqlc.arg(provider_user_id);

-- name: GetActor :one
SELECT id, project_id, provider, provider_tenant_id, provider_user_id, display_name, metadata, created_at, updated_at
FROM actors
WHERE project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(id);

-- name: ListActors :many
SELECT id, project_id, provider, provider_tenant_id, provider_user_id, display_name, metadata, created_at, updated_at
FROM actors
WHERE project_id = sqlc.arg(project_id)
  AND (sqlc.arg(provider)::text = '' OR provider = sqlc.arg(provider))
  AND (sqlc.arg(provider_tenant_id)::text = '' OR provider_tenant_id = sqlc.arg(provider_tenant_id))
  AND (sqlc.arg(provider_user_id)::text = '' OR provider_user_id = sqlc.arg(provider_user_id))
  AND (
    sqlc.narg(cursor_created_at)::timestamptz IS NULL
    OR (created_at, id) > (sqlc.narg(cursor_created_at)::timestamptz, sqlc.narg(cursor_id)::uuid)
  )
ORDER BY created_at ASC, id ASC
LIMIT sqlc.arg(row_limit)::bigint;

-- name: ListActorDisplayNames :many
SELECT provider_user_id, coalesce(display_name, '') AS display_name
FROM actors
WHERE project_id = sqlc.arg(project_id)
  AND provider = sqlc.arg(provider)
  AND provider_tenant_id IS NOT DISTINCT FROM sqlc.narg(provider_tenant_id)
  AND provider_user_id = ANY(sqlc.arg(provider_user_ids)::text[])
  AND display_name IS NOT NULL;

-- name: UpdateActorDisplayName :execrows
UPDATE actors
SET display_name = sqlc.arg(display_name)::text,
    updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id)
  AND provider = sqlc.arg(provider)
  AND provider_tenant_id IS NOT DISTINCT FROM sqlc.narg(provider_tenant_id)
  AND provider_user_id = sqlc.arg(provider_user_id)
  AND display_name IS DISTINCT FROM sqlc.arg(display_name)::text;

-- name: ActorMatchesIntegrationTarget :one
SELECT EXISTS (
  SELECT 1
  FROM actors actor
  JOIN integration_targets target
    ON target.project_id = sqlc.arg(project_id)
   AND target.agent_id = sqlc.arg(agent_id)
   AND target.id = sqlc.arg(integration_target_id)
   AND target.deleted_at IS NULL
  JOIN integration_installs install
    ON install.project_id = target.project_id
   AND install.id = target.integration_install_id
   AND install.state = 'active'
   AND install.deleted_at IS NULL
  WHERE actor.id = sqlc.arg(actor_id)
    AND actor.project_id = target.project_id
    AND actor.provider = install.provider
    AND actor.provider_tenant_id = install.provider_tenant_id
) AS matches;
