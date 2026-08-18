-- name: InsertModelProviderConfig :one
INSERT INTO model_provider_configs(
  org_id, management_kind, name, api_format, api_variant, base_url, endpoint_path,
  request_timeout_ms, auth_kind, auth_options, credential_secret_id,
  created_at, updated_at
)
SELECT org.id, sqlc.arg(management_kind), sqlc.arg(name), sqlc.arg(api_format), sqlc.arg(api_variant),
       sqlc.arg(base_url), sqlc.arg(endpoint_path),
       sqlc.arg(request_timeout_ms), sqlc.arg(auth_kind), sqlc.arg(auth_options),
       sqlc.arg(credential_secret_id),
       transaction_timestamp(), transaction_timestamp()
FROM orgs org
JOIN secrets credential ON credential.org_id = org.id
  AND credential.id = sqlc.arg(credential_secret_id)
  AND credential.management_kind = sqlc.arg(management_kind)
  AND credential.owner_kind = 'org'
  AND credential.kind = 'generic'
WHERE org.id = sqlc.arg(org_id)
ON CONFLICT (org_id, name) WHERE deleted_at IS NULL DO NOTHING
RETURNING id, org_id, management_kind, name, api_format, api_variant, base_url, endpoint_path,
          request_timeout_ms, auth_kind, auth_options,
          credential_secret_id, deleted_at, created_at, updated_at;

-- name: GetModelProviderConfig :one
SELECT id, org_id, management_kind, name, api_format, api_variant, base_url, endpoint_path,
       request_timeout_ms, auth_kind, auth_options,
       credential_secret_id, deleted_at,
       created_at, updated_at
FROM model_provider_configs
WHERE org_id = sqlc.arg(org_id)
  AND id = sqlc.arg(id)
  AND deleted_at IS NULL;

-- name: LockModelProviderConfigForConfiguredModelCreate :one
SELECT id, management_kind, api_format
FROM model_provider_configs
WHERE org_id = sqlc.arg(org_id)
  AND id = sqlc.arg(id)
  AND deleted_at IS NULL
FOR SHARE;

-- name: GetModelProviderConfigByName :one
SELECT id, org_id, management_kind, name, api_format, api_variant, base_url, endpoint_path,
       request_timeout_ms, auth_kind, auth_options,
       credential_secret_id, deleted_at,
       created_at, updated_at
FROM model_provider_configs
WHERE org_id = sqlc.arg(org_id)
  AND name = sqlc.arg(name)
  AND deleted_at IS NULL;

-- name: LockModelProviderConfigForMutation :one
SELECT id, org_id, management_kind, name, api_format, api_variant, base_url, endpoint_path,
       request_timeout_ms, auth_kind, auth_options,
       credential_secret_id, deleted_at,
       created_at, updated_at
FROM model_provider_configs
WHERE org_id = sqlc.arg(org_id)
  AND id = sqlc.arg(id)
  AND deleted_at IS NULL
FOR UPDATE;

-- name: ListModelProviderConfigs :many
WITH listed AS (
 SELECT id, org_id, management_kind, name, api_format, api_variant, base_url, endpoint_path,
        request_timeout_ms, auth_kind, auth_options,
        credential_secret_id, deleted_at, created_at, updated_at,
        CASE sqlc.arg(sort_field)::text
          WHEN 'name' THEN lower(name)
          WHEN 'created_at' THEN to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US')
          WHEN 'updated_at' THEN to_char(updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US')
        END::text AS sort_key,
        false AS sort_is_null
 FROM model_provider_configs
 WHERE org_id = sqlc.arg(org_id)
   AND deleted_at IS NULL
   AND (sqlc.arg(name_pattern)::text = '' OR name ILIKE sqlc.arg(name_pattern)::text ESCAPE '\')
)
SELECT id, org_id, management_kind, name, api_format, api_variant, base_url, endpoint_path,
       request_timeout_ms, auth_kind, auth_options,
       credential_secret_id, deleted_at, created_at, updated_at,
       sort_key, sort_is_null
FROM listed
WHERE sqlc.arg(cursor_set)::boolean = false
   OR (sqlc.arg(sort_desc)::boolean = false AND (sort_key, id) > (sqlc.arg(cursor_key)::text, sqlc.arg(cursor_id)::uuid))
   OR (sqlc.arg(sort_desc)::boolean = true AND (sort_key, id) < (sqlc.arg(cursor_key)::text, sqlc.arg(cursor_id)::uuid))
ORDER BY CASE WHEN NOT sqlc.arg(sort_desc)::boolean THEN sort_key END ASC,
         CASE WHEN sqlc.arg(sort_desc)::boolean THEN sort_key END DESC,
         CASE WHEN NOT sqlc.arg(sort_desc)::boolean THEN id END ASC,
         CASE WHEN sqlc.arg(sort_desc)::boolean THEN id END DESC
LIMIT sqlc.arg(row_limit)::bigint;

-- name: ListClusterManagedModelProviderConfigsByName :many
SELECT id, org_id, management_kind, name, api_format, api_variant, base_url, endpoint_path,
       request_timeout_ms, auth_kind, auth_options,
       credential_secret_id, deleted_at, created_at, updated_at
FROM model_provider_configs
WHERE name = sqlc.arg(name)
  AND management_kind = 'cluster'
  AND deleted_at IS NULL
ORDER BY org_id, id;

-- name: UpdateModelProviderConfig :one
UPDATE model_provider_configs config
SET base_url = sqlc.arg(base_url),
    endpoint_path = sqlc.arg(endpoint_path),
    request_timeout_ms = sqlc.arg(request_timeout_ms),
    auth_kind = sqlc.arg(auth_kind),
    auth_options = sqlc.arg(auth_options),
    credential_secret_id = sqlc.arg(credential_secret_id),
    updated_at = statement_timestamp()
FROM secrets credential
WHERE config.org_id = sqlc.arg(org_id)
  AND config.id = sqlc.arg(id)
  AND config.deleted_at IS NULL
  AND config.management_kind = sqlc.arg(management_kind)
  AND credential.org_id = config.org_id
  AND credential.id = sqlc.arg(credential_secret_id)
  AND credential.management_kind = config.management_kind
  AND credential.owner_kind = 'org'
  AND credential.kind = 'generic'
RETURNING config.id, config.org_id, config.management_kind,
          config.name, config.api_format,
          config.api_variant, config.base_url, config.endpoint_path,
          config.request_timeout_ms, config.auth_kind,
          config.auth_options, config.credential_secret_id, config.deleted_at,
          config.created_at, config.updated_at;

-- name: DeleteModelProviderConfig :one
-- Clearing the credential releases the secret for deletion.
UPDATE model_provider_configs
SET deleted_at = statement_timestamp(),
    credential_secret_id = NULL,
    updated_at = statement_timestamp()
WHERE model_provider_configs.org_id = sqlc.arg(org_id)
  AND model_provider_configs.id = sqlc.arg(id)
  AND model_provider_configs.deleted_at IS NULL
  AND model_provider_configs.management_kind = 'tenant'
  AND NOT EXISTS (
    SELECT 1
    FROM configured_models configured_model
    WHERE configured_model.org_id = model_provider_configs.org_id
      AND configured_model.model_provider_config_id = model_provider_configs.id
      AND configured_model.deleted_at IS NULL
  )
RETURNING id, org_id, management_kind, name, api_format, api_variant, base_url, endpoint_path,
          request_timeout_ms, auth_kind, auth_options,
          credential_secret_id, deleted_at, created_at, updated_at;

-- name: ModelProviderConfigHasActiveModels :one
SELECT EXISTS (
  SELECT 1
  FROM configured_models configured_model
  WHERE configured_model.org_id = sqlc.arg(org_id)
    AND configured_model.model_provider_config_id = sqlc.arg(id)
    AND configured_model.deleted_at IS NULL
) AS has_active_models;

-- name: InsertConfiguredModel :one
WITH ids AS (
  SELECT uuidv7() AS configured_model_id, uuidv7() AS revision_id
),
parent_config AS (
  SELECT config.org_id, config.id
  FROM model_provider_configs config
  WHERE config.org_id = sqlc.arg(org_id)
    AND config.id = sqlc.arg(model_provider_config_id)
    AND config.deleted_at IS NULL
),
configured_model AS (
  INSERT INTO configured_models(
    id, org_id, model_provider_config_id, management_kind, name, current_revision_id,
    created_at, updated_at
  )
  SELECT ids.configured_model_id, config.org_id, config.id,
         sqlc.arg(management_kind)::text, sqlc.arg(name),
         ids.revision_id, statement_timestamp(), statement_timestamp()
  FROM ids
  JOIN parent_config config ON true
  ON CONFLICT (model_provider_config_id, name) WHERE deleted_at IS NULL DO NOTHING
  RETURNING id, org_id, model_provider_config_id, management_kind, name, current_revision_id,
            deleted_at, created_at, updated_at
),
revision AS (
  INSERT INTO configured_model_revisions(
    id, org_id, configured_model_id, model_provider_config_id, provider_model_slug,
    context_window_tokens, max_output_tokens, default_max_output_tokens,
        default_cache_retention, supports_tools, supports_reasoning, default_reasoning_effort,
        supported_reasoning_efforts, input_modalities, output_modalities,
        api_variant_options, created_at
  )
  SELECT configured_model.current_revision_id, configured_model.org_id, configured_model.id,
         configured_model.model_provider_config_id, sqlc.arg(provider_model_slug),
         sqlc.arg(context_window_tokens), sqlc.arg(max_output_tokens),
         sqlc.narg(default_max_output_tokens),
         sqlc.narg(default_cache_retention), sqlc.arg(supports_tools)::bool,
         sqlc.arg(supports_reasoning)::bool, sqlc.arg(default_reasoning_effort)::text,
             sqlc.arg(supported_reasoning_efforts)::text[],
             sqlc.arg(input_modalities)::text[], sqlc.arg(output_modalities)::text[],
             sqlc.arg(api_variant_options), statement_timestamp()
  FROM configured_model
  RETURNING id, org_id, configured_model_id, model_provider_config_id,
            provider_model_slug, context_window_tokens, max_output_tokens,
            default_max_output_tokens,
                default_cache_retention, supports_tools, supports_reasoning,
                default_reasoning_effort, supported_reasoning_efforts,
                input_modalities, output_modalities, api_variant_options, created_at
)
SELECT configured_model.id, configured_model.org_id, configured_model.model_provider_config_id,
       configured_model.management_kind, configured_model.name,
       configured_model.current_revision_id, revision.provider_model_slug,
       revision.context_window_tokens, revision.max_output_tokens,
       revision.default_max_output_tokens,
       revision.default_cache_retention, revision.supports_tools, revision.supports_reasoning,
       revision.default_reasoning_effort, revision.supported_reasoning_efforts,
           revision.input_modalities, revision.output_modalities, revision.api_variant_options,
       configured_model.deleted_at, configured_model.created_at, configured_model.updated_at,
       revision.created_at AS revision_created_at
FROM configured_model
JOIN revision ON revision.configured_model_id = configured_model.id;

-- name: GetConfiguredModel :one
SELECT configured_model.id, configured_model.org_id, configured_model.model_provider_config_id,
       configured_model.management_kind, configured_model.name,
       configured_model.current_revision_id, revision.provider_model_slug,
       revision.context_window_tokens, revision.max_output_tokens,
       revision.default_max_output_tokens,
       revision.default_cache_retention, revision.supports_tools, revision.supports_reasoning,
       revision.default_reasoning_effort, revision.supported_reasoning_efforts,
           revision.input_modalities, revision.output_modalities, revision.api_variant_options,
       configured_model.deleted_at, configured_model.created_at, configured_model.updated_at,
       revision.created_at AS revision_created_at
FROM configured_models configured_model
JOIN configured_model_revisions revision ON revision.org_id = configured_model.org_id
  AND revision.configured_model_id = configured_model.id
  AND revision.id = configured_model.current_revision_id
JOIN model_provider_configs provider_config ON provider_config.org_id = configured_model.org_id
  AND provider_config.id = configured_model.model_provider_config_id
  AND provider_config.deleted_at IS NULL
WHERE configured_model.org_id = sqlc.arg(org_id)
  AND configured_model.id = sqlc.arg(id)
  AND configured_model.deleted_at IS NULL;

-- name: GetConfiguredModelDisplay :one
SELECT configured_model.id, configured_model.org_id, configured_model.model_provider_config_id,
       configured_model.management_kind, configured_model.name,
       configured_model.current_revision_id, revision.provider_model_slug,
       revision.context_window_tokens, revision.max_output_tokens,
       revision.default_max_output_tokens,
       revision.default_cache_retention, revision.supports_tools, revision.supports_reasoning,
       revision.default_reasoning_effort, revision.supported_reasoning_efforts,
           revision.input_modalities, revision.output_modalities, revision.api_variant_options,
       configured_model.deleted_at, configured_model.created_at, configured_model.updated_at,
       revision.created_at AS revision_created_at
FROM configured_models configured_model
JOIN configured_model_revisions revision ON revision.org_id = configured_model.org_id
  AND revision.configured_model_id = configured_model.id
  AND revision.id = configured_model.current_revision_id
JOIN model_provider_configs provider_config ON provider_config.org_id = configured_model.org_id
  AND provider_config.id = configured_model.model_provider_config_id
WHERE configured_model.org_id = sqlc.arg(org_id)
  AND configured_model.id = sqlc.arg(id);

-- name: GetConfiguredModelByName :one
SELECT configured_model.id, configured_model.org_id, configured_model.model_provider_config_id,
       configured_model.management_kind, configured_model.name,
       configured_model.current_revision_id, revision.provider_model_slug,
       revision.context_window_tokens, revision.max_output_tokens,
       revision.default_max_output_tokens,
       revision.default_cache_retention, revision.supports_tools, revision.supports_reasoning,
       revision.default_reasoning_effort, revision.supported_reasoning_efforts,
           revision.input_modalities, revision.output_modalities, revision.api_variant_options,
       configured_model.deleted_at, configured_model.created_at, configured_model.updated_at,
       revision.created_at AS revision_created_at
FROM configured_models configured_model
JOIN configured_model_revisions revision ON revision.org_id = configured_model.org_id
  AND revision.configured_model_id = configured_model.id
  AND revision.id = configured_model.current_revision_id
JOIN model_provider_configs provider_config ON provider_config.org_id = configured_model.org_id
  AND provider_config.id = configured_model.model_provider_config_id
  AND provider_config.deleted_at IS NULL
WHERE configured_model.org_id = sqlc.arg(org_id)
  AND configured_model.model_provider_config_id = sqlc.arg(model_provider_config_id)
  AND configured_model.name = sqlc.arg(name)
  AND configured_model.deleted_at IS NULL;

-- name: ListConfiguredModels :many
SELECT configured_model.id, configured_model.org_id, configured_model.model_provider_config_id,
       configured_model.management_kind, configured_model.name,
       configured_model.current_revision_id, revision.provider_model_slug,
       revision.context_window_tokens, revision.max_output_tokens,
       revision.default_max_output_tokens,
       revision.default_cache_retention, revision.supports_tools, revision.supports_reasoning,
       revision.default_reasoning_effort, revision.supported_reasoning_efforts,
           revision.input_modalities, revision.output_modalities, revision.api_variant_options,
       configured_model.deleted_at, configured_model.created_at, configured_model.updated_at,
       revision.created_at AS revision_created_at
FROM configured_models configured_model
JOIN configured_model_revisions revision ON revision.org_id = configured_model.org_id
  AND revision.configured_model_id = configured_model.id
  AND revision.id = configured_model.current_revision_id
JOIN model_provider_configs provider_config ON provider_config.org_id = configured_model.org_id
  AND provider_config.id = configured_model.model_provider_config_id
  AND provider_config.deleted_at IS NULL
WHERE configured_model.org_id = sqlc.arg(org_id)
  AND configured_model.model_provider_config_id = sqlc.arg(model_provider_config_id)
  AND configured_model.deleted_at IS NULL
  AND (
    sqlc.narg(cursor_created_at)::timestamptz IS NULL
    OR (configured_model.created_at, configured_model.id) < (sqlc.narg(cursor_created_at)::timestamptz, sqlc.narg(cursor_id)::uuid)
  )
ORDER BY configured_model.created_at DESC, configured_model.id DESC
LIMIT sqlc.arg(row_limit)::bigint;

-- name: LockConfiguredModelForUse :one
SELECT configured_model.id, configured_model.org_id, configured_model.model_provider_config_id,
       configured_model.management_kind, configured_model.name,
       configured_model.current_revision_id, revision.provider_model_slug,
       revision.context_window_tokens, revision.max_output_tokens,
       revision.default_max_output_tokens,
       revision.default_cache_retention, revision.supports_tools, revision.supports_reasoning,
       revision.default_reasoning_effort, revision.supported_reasoning_efforts,
           revision.input_modalities, revision.output_modalities, revision.api_variant_options,
       configured_model.deleted_at, configured_model.created_at, configured_model.updated_at,
       revision.created_at AS revision_created_at
FROM configured_models configured_model
JOIN configured_model_revisions revision ON revision.org_id = configured_model.org_id
  AND revision.configured_model_id = configured_model.id
  AND revision.id = configured_model.current_revision_id
JOIN model_provider_configs provider_config ON provider_config.org_id = configured_model.org_id
  AND provider_config.id = configured_model.model_provider_config_id
  AND provider_config.deleted_at IS NULL
WHERE configured_model.org_id = sqlc.arg(org_id)
  AND configured_model.id = sqlc.arg(id)
  AND configured_model.deleted_at IS NULL
FOR SHARE OF configured_model;

-- name: LockConfiguredModelForMutation :one
SELECT configured_model.id, configured_model.org_id, configured_model.model_provider_config_id,
       configured_model.name, configured_model.current_revision_id, configured_model.deleted_at,
       configured_model.created_at, configured_model.updated_at, configured_model.management_kind
FROM configured_models configured_model
JOIN model_provider_configs provider_config ON provider_config.org_id = configured_model.org_id
  AND provider_config.id = configured_model.model_provider_config_id
  AND provider_config.deleted_at IS NULL
WHERE configured_model.org_id = sqlc.arg(org_id)
  AND configured_model.id = sqlc.arg(id)
  AND configured_model.deleted_at IS NULL
FOR UPDATE OF configured_model;

-- name: LockConfiguredModelForDelete :one
SELECT configured_model.id, configured_model.org_id, configured_model.model_provider_config_id,
       configured_model.name, configured_model.current_revision_id, configured_model.deleted_at,
       configured_model.created_at, configured_model.updated_at, configured_model.management_kind
FROM configured_models configured_model
WHERE configured_model.org_id = sqlc.arg(org_id)
  AND configured_model.id = sqlc.arg(id)
  AND configured_model.deleted_at IS NULL
FOR UPDATE OF configured_model;

-- name: UpdateConfiguredModel :one
WITH target_configured_model AS (
  SELECT configured_model.id, configured_model.org_id, configured_model.model_provider_config_id
  FROM configured_models configured_model
  JOIN model_provider_configs provider_config ON provider_config.org_id = configured_model.org_id
    AND provider_config.id = configured_model.model_provider_config_id
    AND provider_config.deleted_at IS NULL
  WHERE configured_model.org_id = sqlc.arg(org_id)
    AND configured_model.model_provider_config_id = sqlc.arg(model_provider_config_id)
    AND configured_model.id = sqlc.arg(id)
    AND configured_model.management_kind = sqlc.arg(management_kind)
    AND configured_model.deleted_at IS NULL
),
revision AS (
  INSERT INTO configured_model_revisions(
    org_id, configured_model_id, model_provider_config_id, provider_model_slug,
    context_window_tokens, max_output_tokens, default_max_output_tokens,
        default_cache_retention, supports_tools, supports_reasoning, default_reasoning_effort,
        supported_reasoning_efforts, input_modalities, output_modalities,
        api_variant_options, created_at
  )
  SELECT target_configured_model.org_id, target_configured_model.id,
         target_configured_model.model_provider_config_id, sqlc.arg(provider_model_slug),
         sqlc.arg(context_window_tokens), sqlc.arg(max_output_tokens),
         sqlc.narg(default_max_output_tokens),
         sqlc.narg(default_cache_retention), sqlc.arg(supports_tools)::bool,
         sqlc.arg(supports_reasoning)::bool, sqlc.arg(default_reasoning_effort)::text,
             sqlc.arg(supported_reasoning_efforts)::text[],
             sqlc.arg(input_modalities)::text[], sqlc.arg(output_modalities)::text[],
             sqlc.arg(api_variant_options), statement_timestamp()
  FROM target_configured_model
  RETURNING id, org_id, configured_model_id, model_provider_config_id,
            provider_model_slug, context_window_tokens, max_output_tokens,
            default_max_output_tokens,
                default_cache_retention, supports_tools, supports_reasoning,
                default_reasoning_effort, supported_reasoning_efforts,
                input_modalities, output_modalities, api_variant_options, created_at
),
configured_model AS (
  UPDATE configured_models
  SET name = sqlc.arg(name),
      current_revision_id = revision.id,
      updated_at = revision.created_at
  FROM revision
  WHERE configured_models.org_id = revision.org_id
    AND configured_models.id = revision.configured_model_id
  RETURNING configured_models.id, configured_models.org_id,
            configured_models.model_provider_config_id, configured_models.management_kind,
            configured_models.name,
            configured_models.current_revision_id, configured_models.deleted_at,
            configured_models.created_at, configured_models.updated_at
)
SELECT configured_model.id, configured_model.org_id, configured_model.model_provider_config_id,
       configured_model.management_kind, configured_model.name,
       configured_model.current_revision_id, revision.provider_model_slug,
       revision.context_window_tokens, revision.max_output_tokens,
       revision.default_max_output_tokens,
       revision.default_cache_retention, revision.supports_tools, revision.supports_reasoning,
       revision.default_reasoning_effort, revision.supported_reasoning_efforts,
           revision.input_modalities, revision.output_modalities, revision.api_variant_options,
       configured_model.deleted_at, configured_model.created_at, configured_model.updated_at,
       revision.created_at AS revision_created_at
FROM configured_model
JOIN revision ON revision.configured_model_id = configured_model.id;

-- name: RenameConfiguredModel :one
WITH configured_model AS (
  UPDATE configured_models configured_model
  SET name = sqlc.arg(name),
      updated_at = statement_timestamp()
  FROM model_provider_configs provider_config
  WHERE configured_model.org_id = sqlc.arg(org_id)
    AND configured_model.model_provider_config_id = sqlc.arg(model_provider_config_id)
    AND configured_model.id = sqlc.arg(id)
    AND configured_model.deleted_at IS NULL
    AND provider_config.org_id = configured_model.org_id
    AND provider_config.id = configured_model.model_provider_config_id
    AND provider_config.deleted_at IS NULL
    AND configured_model.management_kind = 'tenant'
  RETURNING configured_model.id, configured_model.org_id, configured_model.model_provider_config_id,
            configured_model.management_kind, configured_model.name,
            configured_model.current_revision_id, configured_model.deleted_at,
            configured_model.created_at, configured_model.updated_at
)
SELECT configured_model.id, configured_model.org_id, configured_model.model_provider_config_id,
       configured_model.management_kind, configured_model.name,
       configured_model.current_revision_id, revision.provider_model_slug,
       revision.context_window_tokens, revision.max_output_tokens,
       revision.default_max_output_tokens,
       revision.default_cache_retention, revision.supports_tools, revision.supports_reasoning,
       revision.default_reasoning_effort, revision.supported_reasoning_efforts,
           revision.input_modalities, revision.output_modalities, revision.api_variant_options,
       configured_model.deleted_at, configured_model.created_at, configured_model.updated_at,
       revision.created_at AS revision_created_at
FROM configured_model
JOIN configured_model_revisions revision ON revision.org_id = configured_model.org_id
  AND revision.configured_model_id = configured_model.id
  AND revision.id = configured_model.current_revision_id;

-- name: DeleteConfiguredModel :one
UPDATE configured_models
SET deleted_at = statement_timestamp(),
    updated_at = statement_timestamp()
WHERE configured_models.org_id = sqlc.arg(org_id)
  AND configured_models.id = sqlc.arg(id)
  AND configured_models.deleted_at IS NULL
  AND configured_models.management_kind = sqlc.arg(management_kind)
RETURNING id, org_id, model_provider_config_id, management_kind, name, current_revision_id,
          deleted_at, created_at, updated_at;

-- name: GetConfiguredModelReferenceState :one
SELECT EXISTS (
         SELECT 1
         FROM projects project
         JOIN agents agent ON agent.project_id = project.id
         JOIN agent_configs config ON config.project_id = agent.project_id
           AND config.id = agent.current_config_id
         WHERE project.org_id = sqlc.arg(org_id)
           AND agent.state = 'active'
           AND config.configured_model_id = sqlc.arg(id)
       ) AS used_by_active_agent,
       EXISTS (
         SELECT 1
         FROM projects project
         JOIN agent_profiles profile ON profile.project_id = project.id
         JOIN agent_profile_versions version ON version.project_id = profile.project_id
           AND version.profile_id = profile.id
           AND version.id = profile.current_version_id
           AND version.deleted_at IS NULL
         JOIN agent_configs config ON config.project_id = version.project_id
           AND config.id = version.agent_config_id
         WHERE project.org_id = sqlc.arg(org_id)
           AND profile.deleted_at IS NULL
           AND config.configured_model_id = sqlc.arg(id)
       ) AS used_by_agent_profile;

-- name: DeleteProjectModelGrantsForConfiguredModel :execrows
DELETE FROM project_model_grants
WHERE org_id = sqlc.arg(org_id)
  AND configured_model_id = sqlc.arg(id);

-- name: ConfiguredModelHasActiveGrants :one
SELECT EXISTS (
  SELECT 1
  FROM project_model_grants model_grant
  WHERE model_grant.org_id = sqlc.arg(org_id)
    AND model_grant.configured_model_id = sqlc.arg(id)
) AS has_active_grants;

-- name: UpsertProjectModelGrant :one
INSERT INTO project_model_grants(
  org_id, project_id, configured_model_id,
  context_window_tokens, max_output_tokens, default_max_output_tokens,
  default_cache_retention, supports_tools, supports_reasoning,
  default_reasoning_effort, supported_reasoning_efforts,
  input_modalities, output_modalities,
  created_at, updated_at
)
SELECT project.org_id, project.id, configured_model.id,
       sqlc.narg(context_window_tokens), sqlc.narg(max_output_tokens),
       sqlc.narg(default_max_output_tokens),
       sqlc.narg(default_cache_retention), sqlc.narg(supports_tools),
       sqlc.narg(supports_reasoning), sqlc.arg(default_reasoning_effort)::text,
       sqlc.arg(supported_reasoning_efforts)::text[],
       sqlc.arg(input_modalities)::text[], sqlc.arg(output_modalities)::text[],
       statement_timestamp(), statement_timestamp()
FROM projects project
JOIN configured_models configured_model ON configured_model.org_id = project.org_id
  AND configured_model.id = sqlc.arg(configured_model_id)
  AND configured_model.deleted_at IS NULL
JOIN model_provider_configs provider_config ON provider_config.org_id = configured_model.org_id
  AND provider_config.id = configured_model.model_provider_config_id
  AND provider_config.deleted_at IS NULL
WHERE project.org_id = sqlc.arg(org_id)
  AND project.id = sqlc.arg(project_id)
ON CONFLICT (project_id, configured_model_id)
DO UPDATE SET id = project_model_grants.id
RETURNING id, org_id, project_id, configured_model_id,
          context_window_tokens, max_output_tokens, default_max_output_tokens,
          default_cache_retention, supports_tools, supports_reasoning,
          default_reasoning_effort, supported_reasoning_efforts,
          input_modalities, output_modalities,
          created_at, updated_at, xmax = 0 AS created;

-- name: GetActiveProjectModelGrantForConfiguredModel :one
SELECT grant_row.id, grant_row.org_id, grant_row.project_id,
       grant_row.configured_model_id,
       grant_row.context_window_tokens, grant_row.max_output_tokens,
       grant_row.default_max_output_tokens,
       grant_row.default_cache_retention, grant_row.supports_tools,
       grant_row.supports_reasoning, grant_row.default_reasoning_effort,
       grant_row.supported_reasoning_efforts,
       grant_row.input_modalities, grant_row.output_modalities,
       grant_row.created_at, grant_row.updated_at
FROM project_model_grants grant_row
JOIN configured_models configured_model ON configured_model.org_id = grant_row.org_id
  AND configured_model.id = grant_row.configured_model_id
  AND configured_model.deleted_at IS NULL
JOIN model_provider_configs provider_config ON provider_config.org_id = configured_model.org_id
  AND provider_config.id = configured_model.model_provider_config_id
  AND provider_config.deleted_at IS NULL
WHERE grant_row.org_id = sqlc.arg(org_id)
  AND grant_row.project_id = sqlc.arg(project_id)
  AND grant_row.configured_model_id = sqlc.arg(configured_model_id);

-- name: GetProjectModelGrant :one
SELECT id, org_id, project_id, configured_model_id,
       context_window_tokens, max_output_tokens, default_max_output_tokens,
       default_cache_retention, supports_tools, supports_reasoning,
       default_reasoning_effort, supported_reasoning_efforts,
       input_modalities, output_modalities,
       created_at, updated_at
FROM project_model_grants
WHERE org_id = sqlc.arg(org_id)
  AND project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(id);

-- name: UpdateProjectModelGrant :one
UPDATE project_model_grants
SET context_window_tokens = sqlc.narg(context_window_tokens),
    max_output_tokens = sqlc.narg(max_output_tokens),
    default_max_output_tokens = sqlc.narg(default_max_output_tokens),
    default_cache_retention = sqlc.narg(default_cache_retention),
    supports_tools = sqlc.narg(supports_tools),
    supports_reasoning = sqlc.narg(supports_reasoning),
    default_reasoning_effort = sqlc.arg(default_reasoning_effort)::text,
    supported_reasoning_efforts = sqlc.arg(supported_reasoning_efforts)::text[],
    input_modalities = sqlc.arg(input_modalities)::text[],
    output_modalities = sqlc.arg(output_modalities)::text[],
    updated_at = statement_timestamp()
WHERE org_id = sqlc.arg(org_id)
  AND project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(id)
RETURNING id, org_id, project_id, configured_model_id,
          context_window_tokens, max_output_tokens, default_max_output_tokens,
          default_cache_retention, supports_tools, supports_reasoning,
          default_reasoning_effort, supported_reasoning_efforts,
          input_modalities, output_modalities,
          created_at, updated_at;

-- name: ListProjectModelGrants :many
WITH listed AS (
 SELECT g.id, g.org_id, g.project_id, g.configured_model_id,
       g.context_window_tokens, g.max_output_tokens, g.default_max_output_tokens,
       g.default_cache_retention, g.supports_tools, g.supports_reasoning,
       g.default_reasoning_effort, g.supported_reasoning_efforts,
       g.input_modalities, g.output_modalities,
       g.created_at, g.updated_at,
       configured_model.name AS model_name, configured_model.model_provider_config_id,
       provider_config.name AS provider_config_name,
       configured_model.created_at AS model_created_at, configured_model.updated_at AS model_updated_at,
       CASE sqlc.arg(sort_field)::text WHEN 'name' THEN lower(configured_model.name) WHEN 'created_at' THEN to_char(g.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US') WHEN 'updated_at' THEN to_char(g.updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US') END::text AS sort_key, false AS sort_is_null
 FROM project_model_grants g
 JOIN configured_models configured_model ON configured_model.org_id = g.org_id
  AND configured_model.id = g.configured_model_id
  AND configured_model.deleted_at IS NULL
 JOIN model_provider_configs provider_config ON provider_config.org_id = configured_model.org_id
  AND provider_config.id = configured_model.model_provider_config_id
  AND provider_config.deleted_at IS NULL
 WHERE g.org_id = sqlc.arg(org_id)
  AND g.project_id = sqlc.arg(project_id)
  AND (sqlc.arg(name_pattern)::text = '' OR configured_model.name ILIKE sqlc.arg(name_pattern)::text ESCAPE '\')
)
SELECT id, org_id, project_id, configured_model_id,
 context_window_tokens, max_output_tokens, default_max_output_tokens,
 default_cache_retention, supports_tools, supports_reasoning,
 default_reasoning_effort, supported_reasoning_efforts,
 input_modalities, output_modalities,
 created_at, updated_at,
 model_name, model_provider_config_id, provider_config_name, model_created_at, model_updated_at,
 sort_key, sort_is_null
FROM listed WHERE sqlc.arg(cursor_set)::boolean = false
 OR (sqlc.arg(sort_desc)::boolean = false AND (sort_key, id) > (sqlc.arg(cursor_key)::text, sqlc.arg(cursor_id)::uuid))
 OR (sqlc.arg(sort_desc)::boolean = true AND (sort_key, id) < (sqlc.arg(cursor_key)::text, sqlc.arg(cursor_id)::uuid))
ORDER BY CASE WHEN NOT sqlc.arg(sort_desc)::boolean THEN sort_key END ASC, CASE WHEN sqlc.arg(sort_desc)::boolean THEN sort_key END DESC,
 CASE WHEN NOT sqlc.arg(sort_desc)::boolean THEN id END ASC, CASE WHEN sqlc.arg(sort_desc)::boolean THEN id END DESC
LIMIT sqlc.arg(row_limit)::bigint;

-- name: DeleteProjectModelGrant :one
DELETE FROM project_model_grants
WHERE org_id = sqlc.arg(org_id)
  AND project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(id)
RETURNING id, org_id, project_id, configured_model_id,
          context_window_tokens, max_output_tokens, default_max_output_tokens,
          default_cache_retention, supports_tools, supports_reasoning,
          default_reasoning_effort, supported_reasoning_efforts,
          input_modalities, output_modalities,
          created_at, updated_at;

-- name: GetConfiguredModelRevisionForUse :one
SELECT revision.id, revision.org_id, revision.configured_model_id,
       revision.model_provider_config_id,
       revision.provider_model_slug, revision.context_window_tokens,
       revision.max_output_tokens, revision.default_max_output_tokens,
       revision.default_cache_retention,
       revision.supports_tools, revision.supports_reasoning,
       revision.default_reasoning_effort,
       revision.supported_reasoning_efforts, revision.input_modalities,
           revision.output_modalities, revision.api_variant_options,
       revision.created_at
FROM configured_model_revisions revision
JOIN configured_models configured_model ON configured_model.org_id = revision.org_id
  AND configured_model.id = revision.configured_model_id
  AND configured_model.deleted_at IS NULL
JOIN model_provider_configs provider_config ON provider_config.org_id = revision.org_id
  AND provider_config.id = revision.model_provider_config_id
  AND provider_config.deleted_at IS NULL
WHERE revision.org_id = sqlc.arg(org_id)
  AND revision.id = sqlc.arg(id);

-- name: GetConfiguredModelRevisionDisplay :one
-- @sqlc-vet-disable model-provider-configs-deleted-at configured-models-deleted-at
-- Revision display must resolve even when its parents are soft deleted.
SELECT revision.id, revision.org_id, revision.configured_model_id,
       revision.model_provider_config_id,
       revision.provider_model_slug, revision.context_window_tokens,
       revision.max_output_tokens, revision.default_max_output_tokens,
       revision.default_cache_retention,
       revision.supports_tools, revision.supports_reasoning,
       revision.default_reasoning_effort,
       revision.supported_reasoning_efforts, revision.input_modalities,
           revision.output_modalities, revision.api_variant_options,
       revision.created_at,
       configured_model.name AS configured_model_name,
       provider_config.name AS provider_config_name,
       provider_config.api_format,
       provider_config.api_variant
FROM configured_model_revisions revision
JOIN configured_models configured_model ON configured_model.org_id = revision.org_id
  AND configured_model.id = revision.configured_model_id
JOIN model_provider_configs provider_config ON provider_config.org_id = revision.org_id
  AND provider_config.id = revision.model_provider_config_id
WHERE revision.org_id = sqlc.arg(org_id)
  AND revision.id = sqlc.arg(id);
