-- name: InsertIntegrationInstall :one
INSERT INTO integration_installs(
  org_id, project_id, agent_profile_id, agent_id, installed_by_user_id,
  provider, integration_kind, connection_mode, state,
  provider_tenant_id, provider_account_ref, provider_agent_display_name, credential_secret_id,
  provider_config, provider_identity, provider_metadata,
  last_oauth_flow_id, created_at, updated_at
)
VALUES (
  sqlc.arg(org_id), sqlc.arg(project_id), sqlc.narg(agent_profile_id), sqlc.narg(agent_id),
  sqlc.arg(installed_by_user_id), sqlc.arg(provider), sqlc.arg(integration_kind),
  sqlc.arg(connection_mode), sqlc.arg(state), sqlc.arg(provider_tenant_id),
  sqlc.arg(provider_account_ref), sqlc.arg(provider_agent_display_name), sqlc.narg(credential_secret_id),
  sqlc.arg(provider_config), sqlc.arg(provider_identity), sqlc.arg(provider_metadata),
  sqlc.narg(last_oauth_flow_id), transaction_timestamp(), transaction_timestamp()
)
ON CONFLICT (provider, provider_tenant_id, provider_account_ref) WHERE deleted_at IS NULL DO NOTHING
RETURNING id, org_id, project_id, agent_profile_id, agent_id, installed_by_user_id,
  provider, integration_kind, connection_mode, state,
  provider_tenant_id, provider_account_ref, provider_agent_display_name, credential_secret_id,
  provider_config, provider_identity, provider_metadata,
  last_oauth_flow_id, deleted_at, created_at, updated_at;

-- name: UpdateIntegrationInstall :one
UPDATE integration_installs
SET installed_by_user_id = sqlc.arg(installed_by_user_id),
    connection_mode = sqlc.arg(connection_mode),
    state = sqlc.arg(state),
    provider_agent_display_name = CASE
      WHEN sqlc.arg(provider_agent_display_name)::text = '' THEN provider_agent_display_name
      ELSE sqlc.arg(provider_agent_display_name)::text
    END,
    credential_secret_id = sqlc.narg(credential_secret_id),
    provider_config = sqlc.arg(provider_config),
    provider_identity = sqlc.arg(provider_identity),
    provider_metadata = sqlc.arg(provider_metadata),
    last_oauth_flow_id = coalesce(sqlc.narg(last_oauth_flow_id), last_oauth_flow_id),
    updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(id)
  AND deleted_at IS NULL
  AND (
    sqlc.narg(last_oauth_flow_id)::uuid IS NULL
    OR last_oauth_flow_id IS NULL
    OR last_oauth_flow_id < sqlc.narg(last_oauth_flow_id)::uuid
  )
RETURNING id, org_id, project_id, agent_profile_id, agent_id, installed_by_user_id,
  provider, integration_kind, connection_mode, state,
  provider_tenant_id, provider_account_ref, provider_agent_display_name, credential_secret_id,
  provider_config, provider_identity, provider_metadata,
  last_oauth_flow_id, deleted_at, created_at, updated_at;

-- name: LockIntegrationInstallByProviderAccount :one
SELECT id, org_id, project_id, agent_profile_id, agent_id, installed_by_user_id,
  provider, integration_kind, connection_mode, state,
  provider_tenant_id, provider_account_ref, provider_agent_display_name, credential_secret_id,
  provider_config, provider_identity, provider_metadata,
  last_oauth_flow_id, deleted_at, created_at, updated_at
FROM integration_installs
WHERE provider = sqlc.arg(provider)
  AND provider_tenant_id = sqlc.narg(provider_tenant_id)
  AND provider_account_ref = sqlc.arg(provider_account_ref)
  AND deleted_at IS NULL
FOR UPDATE;

-- name: GetIntegrationInstall :one
SELECT id, org_id, project_id, agent_profile_id, agent_id, installed_by_user_id,
  provider, integration_kind, connection_mode, state,
  provider_tenant_id, provider_account_ref, provider_agent_display_name, credential_secret_id,
  provider_config, provider_identity, provider_metadata,
  last_oauth_flow_id, deleted_at, created_at, updated_at
FROM integration_installs
WHERE project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(id)
  AND deleted_at IS NULL;

-- name: GetIntegrationInstallByID :one
SELECT id, org_id, project_id, agent_profile_id, agent_id, installed_by_user_id,
  provider, integration_kind, connection_mode, state,
  provider_tenant_id, provider_account_ref, provider_agent_display_name, credential_secret_id,
  provider_config, provider_identity, provider_metadata,
  last_oauth_flow_id, deleted_at, created_at, updated_at
FROM integration_installs
WHERE id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: ListIntegrationInstallsForProject :many
WITH listed AS (
SELECT install.id, install.org_id, install.project_id, install.agent_profile_id, install.agent_id,
       install.installed_by_user_id, install.provider, install.integration_kind, install.connection_mode,
       install.state, install.provider_tenant_id, install.provider_account_ref,
       install.provider_agent_display_name, install.credential_secret_id,
       install.provider_config, install.provider_identity, install.provider_metadata,
       install.last_oauth_flow_id, install.created_at, install.updated_at,
       CASE sqlc.arg(sort_field)::text
         WHEN 'name' THEN lower(install.provider_agent_display_name)
         WHEN 'created_at' THEN to_char(install.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US')
         WHEN 'updated_at' THEN to_char(install.updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US')
       END::text AS sort_key
FROM integration_installs install
WHERE install.project_id = sqlc.arg(project_id)
  AND install.deleted_at IS NULL
  AND (sqlc.arg(name_pattern)::text = '' OR install.provider_agent_display_name ILIKE sqlc.arg(name_pattern)::text ESCAPE '\')
  AND (sqlc.narg(agent_profile_id)::uuid IS NULL OR install.agent_profile_id = sqlc.narg(agent_profile_id)::uuid)
)
SELECT id, org_id, project_id, agent_profile_id, agent_id, installed_by_user_id,
       provider, integration_kind, connection_mode, state,
       provider_tenant_id, provider_account_ref, provider_agent_display_name, credential_secret_id,
       provider_config, provider_identity, provider_metadata,
       last_oauth_flow_id, created_at, updated_at, sort_key
FROM listed
WHERE sqlc.arg(cursor_set)::boolean = false
   OR (sqlc.arg(sort_desc)::boolean = false AND (sort_key, id) > (sqlc.arg(cursor_key)::text, sqlc.arg(cursor_id)::uuid))
   OR (sqlc.arg(sort_desc)::boolean = true AND (sort_key, id) < (sqlc.arg(cursor_key)::text, sqlc.arg(cursor_id)::uuid))
ORDER BY CASE WHEN sqlc.arg(sort_desc)::boolean = false THEN sort_key END ASC,
         CASE WHEN sqlc.arg(sort_desc)::boolean = true THEN sort_key END DESC,
         CASE WHEN sqlc.arg(sort_desc)::boolean = false THEN id END ASC,
         CASE WHEN sqlc.arg(sort_desc)::boolean = true THEN id END DESC
LIMIT sqlc.arg(row_limit);

-- name: LockIntegrationInstallForDisable :one
SELECT state, last_oauth_flow_id
FROM integration_installs
WHERE project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(id)
  AND deleted_at IS NULL
FOR UPDATE;

-- name: DisableIntegrationInstall :execrows
UPDATE integration_installs
SET state = 'disabled',
    updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(id)
  AND deleted_at IS NULL
  AND state = 'active'
  AND last_oauth_flow_id IS NOT DISTINCT FROM sqlc.narg(expected_oauth_flow_id)::uuid;

-- name: DeleteIntegrationInstall :execrows
-- Clearing the credential releases the secret for deletion; the install
-- keeps whatever active/disabled state it had as provenance.
UPDATE integration_installs
SET credential_secret_id = NULL, deleted_at = statement_timestamp(), updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: DeleteIntegrationTargets :exec
UPDATE integration_targets SET deleted_at = statement_timestamp(), updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND integration_install_id = sqlc.arg(integration_install_id)
  AND deleted_at IS NULL;

-- name: ClearDeletedIntegrationTargetsFromAgents :exec
-- @sqlc-vet-disable integration-targets-deleted-at
-- Clears agent references to targets that were just soft deleted.
UPDATE agents agent SET integration_target_id = NULL, updated_at = statement_timestamp()
WHERE agent.project_id = sqlc.arg(project_id)
  AND agent.integration_target_id IN (
    SELECT target.id FROM integration_targets target
    WHERE target.project_id = sqlc.arg(project_id)
      AND target.integration_install_id = sqlc.arg(integration_install_id)
  );

-- name: IntegrationOAuthFlowConsumed :one
SELECT EXISTS (
  SELECT 1 FROM integration_installs WHERE last_oauth_flow_id = sqlc.arg(last_oauth_flow_id) AND deleted_at IS NULL
) AS consumed;

-- name: GetIntegrationInstallByProviderAccount :one
SELECT id, org_id, project_id, agent_profile_id, agent_id, installed_by_user_id,
  provider, integration_kind, connection_mode, state,
  provider_tenant_id, provider_account_ref, provider_agent_display_name, credential_secret_id,
  provider_config, provider_identity, provider_metadata,
  last_oauth_flow_id, deleted_at, created_at, updated_at
FROM integration_installs
WHERE provider = sqlc.arg(provider)
  AND provider_tenant_id IS NOT DISTINCT FROM sqlc.narg(provider_tenant_id)
  AND provider_account_ref = sqlc.arg(provider_account_ref)
  AND deleted_at IS NULL;

-- name: InsertIntegrationTarget :one
INSERT INTO integration_targets(
  project_id, agent_id, integration_install_id, target_ref, provider_ref,
  provider_ref_kind, display_name, created_at, updated_at
)
SELECT agent.project_id, agent.id, install.id,
       sqlc.arg(target_ref), sqlc.arg(provider_ref), sqlc.arg(provider_ref_kind),
       sqlc.arg(display_name), transaction_timestamp(), transaction_timestamp()
FROM agents agent
JOIN integration_installs install
  ON install.project_id = agent.project_id
 AND install.id = sqlc.arg(integration_install_id)
 AND install.state = 'active'
 AND install.deleted_at IS NULL
WHERE agent.project_id = sqlc.arg(project_id)
  AND agent.id = sqlc.arg(agent_id)
ON CONFLICT (project_id, integration_install_id, provider_ref) WHERE deleted_at IS NULL DO NOTHING
RETURNING id, project_id, agent_id, integration_install_id, target_ref, provider_ref,
  provider_ref_kind, display_name, provider_metadata, deleted_at, created_at, updated_at;

-- name: GetIntegrationTarget :one
SELECT target.id, project.org_id, target.project_id, target.agent_id, target.integration_install_id, target.target_ref, target.provider_ref,
  target.provider_ref_kind, target.display_name, target.provider_metadata, target.deleted_at, target.created_at, target.updated_at
FROM integration_targets target
JOIN projects project ON project.id = target.project_id
WHERE target.project_id = sqlc.arg(project_id)
  AND target.id = sqlc.arg(id)
  AND target.deleted_at IS NULL;

-- name: GetIntegrationTargetByProviderRef :one
SELECT target.id, project.org_id, target.project_id, target.agent_id, target.integration_install_id, target.target_ref, target.provider_ref,
  target.provider_ref_kind, target.display_name, target.provider_metadata, target.deleted_at, target.created_at, target.updated_at
FROM integration_targets target
JOIN projects project ON project.id = target.project_id
WHERE target.project_id = sqlc.arg(project_id)
  AND target.integration_install_id = sqlc.arg(integration_install_id)
  AND target.provider_ref = sqlc.arg(provider_ref)
  AND target.deleted_at IS NULL;

-- name: UpdateIntegrationTargetDisplayNamesByProviderRefPrefix :execrows
UPDATE integration_targets
SET display_name = sqlc.arg(display_name),
    updated_at = transaction_timestamp()
WHERE project_id = sqlc.arg(project_id)
  AND integration_install_id = sqlc.arg(integration_install_id)
  AND deleted_at IS NULL
  AND split_part(provider_ref, ':', 1) = sqlc.arg(provider_ref_prefix)
  AND display_name IS DISTINCT FROM sqlc.arg(display_name);

-- name: ListIntegrationTargets :many
SELECT target.id,
  target.integration_install_id,
  target.target_ref,
  target.provider_ref,
  target.provider_ref_kind,
  target.display_name,
  install.provider, install.state AS install_state,
  CASE WHEN agent.integration_target_id = target.id THEN true ELSE false END AS is_current
FROM integration_targets target
JOIN agents agent
  ON agent.project_id = target.project_id
 AND agent.id = target.agent_id
JOIN integration_installs install
  ON install.project_id = target.project_id
 AND install.id = target.integration_install_id
 AND install.deleted_at IS NULL
WHERE target.project_id = sqlc.arg(project_id)
  AND target.agent_id = sqlc.arg(agent_id)
  AND target.deleted_at IS NULL
ORDER BY is_current DESC, target.created_at ASC, target.id ASC;

-- name: SetAgentIntegrationTarget :one
UPDATE agents
SET integration_target_id = sqlc.narg(integration_target_id)::uuid,
    updated_at = statement_timestamp()
WHERE agents.project_id = sqlc.arg(project_id)
  AND agents.id = sqlc.arg(agent_id)
  AND (
    sqlc.narg(integration_target_id)::uuid IS NULL
    OR EXISTS (
      SELECT 1
      FROM integration_targets target
      JOIN integration_installs install
        ON install.project_id = target.project_id
       AND install.id = target.integration_install_id
       AND install.state = 'active'
       AND install.deleted_at IS NULL
      WHERE target.project_id = agents.project_id
        AND target.agent_id = agents.id
        AND target.id = sqlc.narg(integration_target_id)::uuid
        AND target.deleted_at IS NULL
    )
  )
RETURNING id, org_id, project_id, state, name,
  agent_profile_id, current_config_id, integration_target_id,
  coalesce(idempotency_key, '') AS idempotency_key,
  next_event_sequence, created_at, updated_at, archived_at;
