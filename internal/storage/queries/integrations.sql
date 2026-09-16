-- Managed upserts serialize the physical installation identity before looking
-- for an existing row or acquiring app/credential locks. Project is deliberately
-- absent: the unique identity belongs to the app across all project callers.
-- JSON tuple encoding distinguishes NULL tenant and delimiter-bearing values.
-- name: LockIntegrationInstallIdentity :exec
SELECT pg_advisory_xact_lock(hashtextextended(
  'integration_install_identity:' || jsonb_build_array(
    sqlc.arg(integration_app_id)::uuid::text,
    sqlc.narg(provider_tenant_id)::text,
    sqlc.arg(provider_account_ref)::text
  )::text, 0
));

-- Unlocked discovery precedes lifecycle/installation row locking. In particular
-- this lookup must not acquire the app SHARE lock held by INSERT triggers.
-- name: GetIntegrationInstallByAppProviderAccount :one
SELECT id, org_id, project_id, installed_by_user_id,
  provider, integration_kind, connection_mode, state,
  provider_tenant_id, provider_account_ref, display_name, credential_secret_id,
  provider_config, provider_identity, metadata,
  last_oauth_flow_id, deleted_at, created_at, updated_at, integration_app_id,
  configuration_revision, installed_by_org_api_key_id
FROM integration_installs
WHERE integration_kind = 'managed'
  AND integration_app_id = sqlc.arg(integration_app_id)::uuid
  AND provider_tenant_id IS NOT DISTINCT FROM sqlc.narg(provider_tenant_id)::text
  AND provider_account_ref = sqlc.arg(provider_account_ref)::text
  AND deleted_at IS NULL;

-- name: InsertIntegrationInstall :one
INSERT INTO integration_installs(
  org_id, project_id, installed_by_user_id,
  provider, integration_kind, connection_mode, state,
  provider_tenant_id, provider_account_ref, display_name, credential_secret_id,
  provider_config, provider_identity, metadata,
  last_oauth_flow_id, created_at, updated_at, integration_app_id, installed_by_org_api_key_id
)
VALUES (
  sqlc.arg(org_id), sqlc.arg(project_id),
  sqlc.narg(installed_by_user_id), sqlc.arg(provider), sqlc.arg(integration_kind),
  sqlc.arg(connection_mode), sqlc.arg(state), sqlc.narg(provider_tenant_id),
  sqlc.arg(provider_account_ref), sqlc.arg(display_name), sqlc.narg(credential_secret_id),
  sqlc.arg(provider_config), sqlc.arg(provider_identity), sqlc.arg(metadata),
  sqlc.narg(last_oauth_flow_id), transaction_timestamp(), transaction_timestamp(),
  sqlc.narg(integration_app_id), sqlc.narg(installed_by_org_api_key_id)
)
ON CONFLICT (integration_app_id, provider_tenant_id, provider_account_ref)
  WHERE integration_kind = 'managed' AND deleted_at IS NULL DO NOTHING
RETURNING id, org_id, project_id, installed_by_user_id,
  provider, integration_kind, connection_mode, state,
  provider_tenant_id, provider_account_ref, display_name, credential_secret_id,
  provider_config, provider_identity, metadata,
  last_oauth_flow_id, deleted_at, created_at, updated_at, integration_app_id,
  configuration_revision, installed_by_org_api_key_id;

-- name: UpdateIntegrationInstall :one
UPDATE integration_installs
SET installed_by_user_id = sqlc.narg(installed_by_user_id),
    installed_by_org_api_key_id = sqlc.narg(installed_by_org_api_key_id),
    connection_mode = sqlc.arg(connection_mode),
    state = sqlc.arg(state),
    display_name = CASE
      WHEN sqlc.arg(display_name)::text = '' THEN display_name
      ELSE sqlc.arg(display_name)::text
    END,
    credential_secret_id = sqlc.narg(credential_secret_id),
    provider_config = sqlc.arg(provider_config),
    provider_identity = sqlc.arg(provider_identity),
    metadata = sqlc.arg(metadata),
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
RETURNING id, org_id, project_id, installed_by_user_id,
  provider, integration_kind, connection_mode, state,
  provider_tenant_id, provider_account_ref, display_name, credential_secret_id,
  provider_config, provider_identity, metadata,
  last_oauth_flow_id, deleted_at, created_at, updated_at, integration_app_id,
  configuration_revision, installed_by_org_api_key_id;

-- name: LockIntegrationInstallByAppProviderAccount :one
SELECT id, org_id, project_id, installed_by_user_id,
  provider, integration_kind, connection_mode, state,
  provider_tenant_id, provider_account_ref, display_name, credential_secret_id,
  provider_config, provider_identity, metadata,
  last_oauth_flow_id, deleted_at, created_at, updated_at, integration_app_id,
  configuration_revision, installed_by_org_api_key_id
FROM integration_installs
WHERE integration_kind = 'managed'
  AND integration_app_id = sqlc.arg(integration_app_id)
  AND provider_tenant_id IS NOT DISTINCT FROM sqlc.narg(provider_tenant_id)
  AND provider_account_ref = sqlc.arg(provider_account_ref)
  AND deleted_at IS NULL
FOR UPDATE;

-- name: GetIntegrationInstall :one
SELECT id, org_id, project_id, installed_by_user_id,
  provider, integration_kind, connection_mode, state,
  provider_tenant_id, provider_account_ref, display_name, credential_secret_id,
  provider_config, provider_identity, metadata,
  last_oauth_flow_id, deleted_at, created_at, updated_at, integration_app_id,
  configuration_revision, installed_by_org_api_key_id
FROM integration_installs
WHERE project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(id)
  AND deleted_at IS NULL;

-- name: LockIntegrationInstallForMutation :one
SELECT id
FROM integration_installs
WHERE project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(id)
  AND deleted_at IS NULL
FOR UPDATE;

-- name: LockIntegrationInstallLifecycleShared :exec
SELECT pg_advisory_xact_lock_shared(
  hashtextextended('integration_install_lifecycle:' || sqlc.arg(install_id)::uuid::text, 0)
);

-- name: LockIntegrationInstallLifecycleExclusive :exec
SELECT pg_advisory_xact_lock(
  hashtextextended('integration_install_lifecycle:' || sqlc.arg(install_id)::uuid::text, 0)
);

-- name: GetIntegrationInstallByID :one
SELECT id, org_id, project_id, installed_by_user_id,
  provider, integration_kind, connection_mode, state,
  provider_tenant_id, provider_account_ref, display_name, credential_secret_id,
  provider_config, provider_identity, metadata,
  last_oauth_flow_id, deleted_at, created_at, updated_at, integration_app_id,
  configuration_revision, installed_by_org_api_key_id
FROM integration_installs
WHERE id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: ListIntegrationInstallsForProject :many
WITH listed AS (
SELECT install.id, install.org_id, install.project_id,
       install.installed_by_user_id, install.provider, install.integration_kind, install.connection_mode,
       install.state, install.provider_tenant_id, install.provider_account_ref,
       install.display_name, install.credential_secret_id,
       install.provider_config, install.provider_identity, install.metadata,
       install.last_oauth_flow_id, install.created_at, install.updated_at,
       install.integration_app_id, install.configuration_revision, install.installed_by_org_api_key_id,
       CASE sqlc.arg(sort_field)::text
         WHEN 'name' THEN lower(install.display_name)
         WHEN 'created_at' THEN to_char(install.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US')
         WHEN 'updated_at' THEN to_char(install.updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US')
       END::text AS sort_key
FROM integration_installs install
WHERE install.project_id = sqlc.arg(project_id)
  AND install.deleted_at IS NULL
  AND (sqlc.arg(name_pattern)::text = '' OR install.display_name ILIKE sqlc.arg(name_pattern)::text ESCAPE '\')
  AND (sqlc.narg(agent_profile_id)::uuid IS NULL
    OR EXISTS (
      SELECT 1 FROM integration_routes route
      WHERE route.project_id = install.project_id AND route.integration_install_id = install.id
        AND route.agent_profile_id = sqlc.narg(agent_profile_id)::uuid AND route.deleted_at IS NULL
    ))
  AND (sqlc.narg(oauth_flow_id)::uuid IS NULL OR install.last_oauth_flow_id = sqlc.narg(oauth_flow_id)::uuid)
)
SELECT id, org_id, project_id, installed_by_user_id,
       provider, integration_kind, connection_mode, state,
       provider_tenant_id, provider_account_ref, display_name, credential_secret_id,
       provider_config, provider_identity, metadata,
       last_oauth_flow_id, created_at, updated_at, integration_app_id,
       configuration_revision, installed_by_org_api_key_id, sort_key
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

-- name: DeleteIntegrationRoutes :exec
UPDATE integration_routes
SET state = 'disabled', deleted_at = statement_timestamp(), updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id)
  AND integration_install_id = sqlc.arg(integration_install_id)
  AND deleted_at IS NULL;

-- name: DeleteIntegrationTargets :exec
UPDATE integration_targets SET deleted_at = statement_timestamp(), updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND integration_install_id = sqlc.arg(integration_install_id)
  AND deleted_at IS NULL;

-- name: ListIntegrationInstallAgentIDsForLifecycle :many
-- @sqlc-vet-disable integration-targets-deleted-at
-- Lock owners of live bindings, current pointers and pending requests before
-- retiring the installation. A revoked binding can still have a pending owner.
SELECT binding.agent_id
FROM integration_target_bindings binding
WHERE binding.project_id = sqlc.arg(project_id)
  AND binding.integration_install_id = sqlc.arg(integration_install_id)
  AND binding.revoked_at IS NULL
UNION
SELECT agent.id
FROM agents agent
JOIN integration_targets target
  ON target.project_id = agent.project_id
 AND target.id = agent.integration_target_id
WHERE agent.project_id = sqlc.arg(project_id)
  AND target.integration_install_id = sqlc.arg(integration_install_id)
UNION
SELECT request.agent_id
FROM external_channel_requests request
WHERE request.project_id = sqlc.arg(project_id)
  AND request.integration_install_id = sqlc.arg(integration_install_id)
  AND request.state = 'pending'
ORDER BY agent_id;

-- name: ClearDeletedIntegrationTargetsFromAgents :exec
-- @sqlc-vet-disable integration-targets-deleted-at
-- Clears agent references before soft deleting the install's targets.
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

-- Slack alone retains its released physical app/workspace edge identity.
-- name: GetSlackIntegrationInstallByIdentity :one
SELECT id, org_id, project_id, installed_by_user_id,
  provider, integration_kind, connection_mode, state,
  provider_tenant_id, provider_account_ref, display_name, credential_secret_id,
  provider_config, provider_identity, metadata,
  last_oauth_flow_id, deleted_at, created_at, updated_at, integration_app_id,
  configuration_revision, installed_by_org_api_key_id
FROM integration_installs
WHERE integration_kind = 'managed' AND provider = 'slack'
  AND provider_tenant_id = sqlc.arg(provider_tenant_id)::text
  AND provider_account_ref = sqlc.arg(provider_account_ref)::text
  AND deleted_at IS NULL;

-- name: LockIntegrationTargetCreateAuthority :one
WITH install_authority AS MATERIALIZED (
  SELECT install.id, install.org_id, install.integration_app_id, install.integration_kind
  FROM integration_installs install
  WHERE install.project_id = sqlc.arg(project_id)
    AND install.id = sqlc.arg(integration_install_id)
    AND install.state = 'active'
    AND install.deleted_at IS NULL
  FOR SHARE OF install
)
SELECT install.id
FROM install_authority install
LEFT JOIN LATERAL (
  SELECT app.id FROM integration_apps app
  WHERE app.id = install.integration_app_id AND app.org_id = install.org_id
    AND app.state = 'active' AND app.deleted_at IS NULL
  FOR SHARE OF app
) app ON true
WHERE (install.integration_kind = 'external' OR (install.integration_kind = 'managed' AND app.id IS NOT NULL));

-- name: InsertIntegrationTarget :one
INSERT INTO integration_targets(
  project_id, integration_install_id, provider_ref,
  provider_ref_kind, parent_channel_id, channel_definition_id, display_name, provider_metadata, created_at, updated_at
)
VALUES (
  sqlc.arg(project_id), sqlc.arg(integration_install_id),
  sqlc.arg(provider_ref), sqlc.arg(provider_ref_kind), sqlc.narg(parent_channel_id), sqlc.arg(channel_definition_id),
  sqlc.arg(display_name), sqlc.arg(provider_metadata),
  transaction_timestamp(), transaction_timestamp()
)
ON CONFLICT (project_id, integration_install_id, provider_ref) WHERE deleted_at IS NULL DO NOTHING
RETURNING id, project_id, integration_install_id, provider_ref,
  provider_ref_kind, display_name, provider_metadata, deleted_at, created_at, updated_at, parent_channel_id, channel_definition_id;

-- name: UpdateResolvedIntegrationTarget :one
UPDATE integration_targets
SET display_name = CASE
      WHEN sqlc.arg(display_name)::text = '' THEN display_name
      ELSE sqlc.arg(display_name)::text
    END,
    provider_metadata = sqlc.arg(provider_metadata),
    updated_at = CASE
      WHEN (
        sqlc.arg(display_name)::text <> ''
        AND display_name IS DISTINCT FROM sqlc.arg(display_name)::text
      ) OR provider_metadata IS DISTINCT FROM sqlc.arg(provider_metadata)::jsonb
      THEN statement_timestamp()
      ELSE updated_at
    END
WHERE project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(id)
  AND provider_ref_kind = sqlc.arg(provider_ref_kind)
  AND deleted_at IS NULL
RETURNING id, project_id, integration_install_id, provider_ref,
  provider_ref_kind, display_name, provider_metadata, deleted_at, created_at, updated_at, parent_channel_id, channel_definition_id;

-- name: GetIntegrationTarget :one
SELECT target.id, project.org_id, target.project_id, target.integration_install_id, target.provider_ref,
  target.provider_ref_kind, target.parent_channel_id, target.channel_definition_id, target.display_name, target.provider_metadata, target.deleted_at, target.created_at, target.updated_at
FROM integration_targets target
JOIN projects project ON project.id = target.project_id
WHERE target.project_id = sqlc.arg(project_id)
  AND target.id = sqlc.arg(id)
  AND target.deleted_at IS NULL;

-- name: GetIntegrationTargetByProviderRef :one
SELECT target.id, project.org_id, target.project_id, target.integration_install_id, target.provider_ref,
  target.provider_ref_kind, target.parent_channel_id, target.channel_definition_id, target.display_name, target.provider_metadata, target.deleted_at, target.created_at, target.updated_at
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
      LEFT JOIN integration_apps app
        ON app.org_id = install.org_id
       AND app.id = install.integration_app_id
       AND app.state = 'active'
       AND app.deleted_at IS NULL
      WHERE (install.integration_kind = 'external' OR (install.integration_kind = 'managed' AND app.id IS NOT NULL))
        AND target.project_id = agents.project_id
        AND target.id = sqlc.narg(integration_target_id)::uuid
        AND target.deleted_at IS NULL
        AND EXISTS (
          SELECT 1 FROM integration_target_bindings binding
          WHERE binding.project_id = agents.project_id
            AND binding.agent_id = agents.id
            AND binding.integration_target_id = target.id
            AND binding.revoked_at IS NULL
            AND (
              binding.integration_route_id IS NULL
              OR EXISTS (
                SELECT 1 FROM integration_routes route
                WHERE route.project_id = binding.project_id
                  AND route.integration_install_id = binding.integration_install_id
                  AND route.id = binding.integration_route_id
                  AND route.state = 'active' AND route.deleted_at IS NULL
              )
            )
        )
    )
  )
RETURNING id, org_id, project_id, state, name,
  agent_profile_id, current_config_id, integration_target_id,
  coalesce(idempotency_key, '') AS idempotency_key,
  next_event_sequence, created_at, updated_at, archived_at,
  parent_agent_id, subagent_key;

-- name: GetAgentCurrentChannelID :one
SELECT integration_target_id
FROM agents
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(agent_id);

-- Only newly admitted inputs update routing. The caller already holds the agent
-- lock, and the input's project-scoped foreign key established its provenance.
-- A retired origin must not block admission or silently retain an older origin.
-- name: SetAgentCurrentChannelFromInput :exec
UPDATE agents
SET integration_target_id = sqlc.arg(channel_id)::uuid, updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(agent_id)
  AND integration_target_id IS DISTINCT FROM sqlc.arg(channel_id)::uuid;

-- name: LockIntegrationChannelParent :one
SELECT id
FROM integration_targets
WHERE project_id = sqlc.arg(project_id)
  AND integration_install_id = sqlc.arg(integration_install_id)
  AND id = sqlc.arg(parent_channel_id)
  AND deleted_at IS NULL
FOR SHARE;

-- name: InsertExternalIntegrationInstall :one
INSERT INTO integration_installs (
  org_id, project_id, installed_by_user_id, installed_by_org_api_key_id,
  integration_kind, connection_mode, state, display_name, metadata, created_at, updated_at
) VALUES (
  sqlc.arg(org_id), sqlc.arg(project_id), sqlc.narg(installed_by_user_id), sqlc.narg(installed_by_org_api_key_id),
  'external', 'api', 'active', sqlc.arg(display_name), sqlc.arg(metadata), statement_timestamp(), statement_timestamp()
)
RETURNING id, org_id, project_id, installed_by_user_id,
  provider, integration_kind, connection_mode, state, provider_tenant_id, provider_account_ref,
  display_name, credential_secret_id, provider_config, provider_identity, metadata,
  last_oauth_flow_id, deleted_at, created_at, updated_at, integration_app_id,
  configuration_revision, installed_by_org_api_key_id;
