-- name: GetProjectByID :one
SELECT id, org_id, name, coalesce(idempotency_key, '') AS idempotency_key, created_at, updated_at
FROM projects
WHERE id = $1
  AND deleted_at IS NULL;

-- name: UpsertAgentConfigByHash :one
WITH inserted_config AS (
INSERT INTO agent_configs(
    org_id, project_id, configured_model_id, definition, source, source_format, source_hash,
    compiled_definition, compiler_version, effective_definition_hash,
    created_at
)
VALUES (
    sqlc.arg(org_id), sqlc.arg(project_id),
    sqlc.arg(configured_model_id),
    sqlc.arg(definition), sqlc.arg(source), sqlc.arg(source_format),
    sqlc.arg(source_hash), sqlc.arg(compiled_definition),
    sqlc.arg(compiler_version), sqlc.arg(effective_definition_hash),
    transaction_timestamp()
)
ON CONFLICT (project_id, effective_definition_hash, source_format, source_hash) DO NOTHING
RETURNING id, org_id, project_id, configured_model_id, definition, source, source_format, source_hash,
          compiled_definition, compiler_version, effective_definition_hash,
          created_at, true AS inserted
)
SELECT id, org_id, project_id, configured_model_id, definition, source, source_format, source_hash,
          compiled_definition, compiler_version, effective_definition_hash,
       created_at, inserted
FROM inserted_config
UNION ALL
SELECT id, org_id, project_id, configured_model_id, definition, source, source_format, source_hash,
          compiled_definition, compiler_version, effective_definition_hash,
       created_at, false AS inserted
FROM agent_configs
WHERE project_id = sqlc.arg(project_id)
  AND effective_definition_hash = sqlc.arg(effective_definition_hash)::text
  AND source_format = sqlc.arg(source_format)::text
  AND source_hash = sqlc.arg(source_hash)::text
LIMIT 1;

-- name: GetAgentConfig :one
SELECT id, org_id, project_id, configured_model_id, definition, source, source_format, source_hash,
          compiled_definition, compiler_version, effective_definition_hash,
       created_at
FROM agent_configs
WHERE project_id = $1 AND id = $2;

-- name: CaptureAgentConfigForModelContext :one
WITH existing_agent AS MATERIALIZED (
  SELECT agent.id, agent.project_id, agent.current_config_id
  FROM agents agent
  WHERE agent.project_id = sqlc.arg(project_id)
    AND agent.id = sqlc.arg(agent_id)
  FOR UPDATE
),
watermark AS MATERIALIZED (
  SELECT coalesce(max(event.sequence), 0)::bigint AS input_event_sequence
  FROM agent_events event
  JOIN existing_agent agent ON agent.id = event.agent_id
  WHERE event.agent_id = sqlc.arg(agent_id)
),
config_change AS MATERIALIZED (
  SELECT input.agent_config_id
  FROM agent_events event
  JOIN agent_inputs input ON input.agent_id = event.agent_id
    AND input.id = event.agent_input_id
  JOIN existing_agent agent ON agent.id = event.agent_id
  JOIN watermark ON true
  WHERE event.sequence <= watermark.input_event_sequence
    AND event.event_kind = 'agent_input'
    AND input.input_kind = 'config_change'
    AND input.state = 'resolved'
    AND input.admitted_event_id = event.id
    AND input.agent_config_id IS NOT NULL
  ORDER BY event.sequence DESC
  LIMIT 1
)
SELECT config.id, config.org_id, config.project_id, config.configured_model_id,
       config.definition, config.source, config.source_format, config.source_hash,
       config.compiled_definition,
       config.compiler_version, config.effective_definition_hash,
       config.created_at,
       watermark.input_event_sequence
FROM existing_agent
JOIN agent_configs config ON config.project_id = existing_agent.project_id
  AND config.id = existing_agent.current_config_id
JOIN watermark ON true
JOIN config_change ON config_change.agent_config_id = existing_agent.current_config_id;

-- name: CaptureAgentConfigForEventWatermark :one
WITH existing_agent AS MATERIALIZED (
  SELECT agent.id, agent.project_id
  FROM agents agent
  WHERE agent.project_id = sqlc.arg(project_id)
    AND agent.id = sqlc.arg(agent_id)
),
config_change AS MATERIALIZED (
  SELECT input.agent_config_id
  FROM agent_events event
  JOIN agent_inputs input ON input.agent_id = event.agent_id
    AND input.id = event.agent_input_id
  JOIN existing_agent agent ON agent.id = event.agent_id
  WHERE event.sequence <= sqlc.arg(input_event_sequence)::bigint
    AND event.event_kind = 'agent_input'
    AND input.input_kind = 'config_change'
    AND input.state = 'resolved'
    AND input.admitted_event_id = event.id
    AND input.agent_config_id IS NOT NULL
  ORDER BY event.sequence DESC
  LIMIT 1
)
SELECT config.id, config.org_id, config.project_id, config.configured_model_id,
       config.definition, config.source, config.source_format, config.source_hash,
       config.compiled_definition,
       config.compiler_version, config.effective_definition_hash,
       config.created_at,
       sqlc.arg(input_event_sequence)::bigint AS input_event_sequence
FROM existing_agent
JOIN config_change ON true
JOIN agent_configs config ON config.project_id = existing_agent.project_id
  AND config.id = config_change.agent_config_id;

-- name: GetAgentConfigByHash :one
SELECT id, org_id, project_id, configured_model_id, definition, source, source_format, source_hash,
          compiled_definition, compiler_version, effective_definition_hash,
       created_at
FROM agent_configs
WHERE project_id = sqlc.arg(project_id)
  AND effective_definition_hash = sqlc.arg(effective_definition_hash)::text
  AND source_format = sqlc.arg(source_format)::text
  AND source_hash = sqlc.arg(source_hash)::text;

-- name: InsertAgentProfile :one
WITH seed AS (
    SELECT uuidv7() AS profile_id,
           uuidv7() AS version_id,
           transaction_timestamp() AS created_at
), inserted_profile AS (
    INSERT INTO agent_profiles(
        id, project_id, name, current_version_id,
        idempotency_key, created_at, updated_at
    )
    SELECT seed.profile_id, sqlc.arg(project_id), sqlc.arg(name),
           seed.version_id, sqlc.narg(idempotency_key),
           seed.created_at, seed.created_at
    FROM seed
    ON CONFLICT (project_id, idempotency_key) DO NOTHING
    RETURNING id, project_id, name, current_version_id,
              idempotency_key, created_at, updated_at
), inserted_version AS (
    INSERT INTO agent_profile_versions(
        id, project_id, profile_id, generation, agent_config_id,
        reason, created_at
    )
    SELECT seed.version_id, profile.project_id, profile.id, 1,
           sqlc.arg(current_config_id), 'create', profile.created_at
    FROM inserted_profile profile
    JOIN seed ON seed.profile_id = profile.id
    RETURNING id, project_id, profile_id, generation, agent_config_id
)
SELECT profile.id, profile.project_id, profile.name,
       version.agent_config_id AS current_config_id,
       version.generation AS current_generation,
       coalesce(profile.idempotency_key, '') AS idempotency_key,
       profile.created_at, profile.updated_at
FROM inserted_profile profile
JOIN inserted_version version
  ON version.project_id = profile.project_id
 AND version.profile_id = profile.id
 AND version.id = profile.current_version_id;

-- name: UpdateAgentCurrentConfig :execrows
UPDATE agents
SET current_config_id = sqlc.arg(agent_config_id),
    updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(agent_id);

-- name: GetAgentProfile :one
SELECT profile.id, project.org_id, profile.project_id, profile.name,
       version.agent_config_id AS current_config_id,
       version.generation AS current_generation,
       coalesce(profile.idempotency_key, '') AS idempotency_key, profile.created_at, profile.updated_at
FROM agent_profiles profile
JOIN projects project ON project.id = profile.project_id
JOIN agent_profile_versions version
  ON version.project_id = profile.project_id
 AND version.profile_id = profile.id
 AND version.id = profile.current_version_id
 AND version.deleted_at IS NULL
WHERE profile.project_id = $1 AND profile.id = $2 AND profile.deleted_at IS NULL;

-- name: GetAgentProfileIDByName :one
SELECT profile.id
FROM agent_profiles profile
WHERE profile.project_id = sqlc.arg(project_id)
  AND profile.name = sqlc.arg(name)::text
  AND profile.deleted_at IS NULL;

-- name: ListAgentProfilesForProject :many
WITH listed AS (
SELECT profile.id, project.org_id AS org_id, profile.project_id, profile.name,
       version.agent_config_id AS current_config_id,
       version.generation AS current_generation,
       coalesce(profile.idempotency_key, '') AS idempotency_key,
       profile.created_at, profile.updated_at,
       config.id AS config_id, config.org_id AS config_org_id,
       config.project_id AS config_project_id,
       config.configured_model_id AS config_configured_model_id,
       config.source AS config_source, config.source_format AS config_source_format,
       config.source_hash AS config_source_hash,
       config.compiled_definition AS config_compiled_definition,
       config.compiler_version AS config_compiler_version,
       config.effective_definition_hash AS config_effective_definition_hash,
       config.created_at AS config_created_at,
       CASE sqlc.arg(sort_field)::text
         WHEN 'name' THEN lower(profile.name)
         WHEN 'created_at' THEN to_char(profile.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US')
         WHEN 'updated_at' THEN to_char(profile.updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US')
         WHEN 'model_provider' THEN lower(provider.name)
         WHEN 'model' THEN lower(model.name)
         WHEN 'api_format' THEN provider.api_format
         WHEN 'api_variant' THEN provider.api_variant
       END::text AS sort_key,
       false AS sort_is_null
FROM agent_profiles profile
JOIN projects project ON project.id = profile.project_id
JOIN agent_profile_versions version
  ON version.project_id = profile.project_id
 AND version.profile_id = profile.id
 AND version.id = profile.current_version_id
 AND version.deleted_at IS NULL
JOIN agent_configs config ON config.project_id = profile.project_id
  AND config.id = version.agent_config_id
JOIN configured_models model ON model.org_id = config.org_id
  AND model.id = config.configured_model_id
JOIN model_provider_configs provider ON provider.org_id = model.org_id
  AND provider.id = model.model_provider_config_id
WHERE profile.project_id = sqlc.arg(project_id)
  AND profile.deleted_at IS NULL
  AND (sqlc.arg(name_pattern)::text = '' OR profile.name ILIKE sqlc.arg(name_pattern)::text ESCAPE '\')
  AND (sqlc.narg(model_provider_config_id)::uuid IS NULL OR provider.id = sqlc.narg(model_provider_config_id)::uuid)
  AND (sqlc.narg(configured_model_id)::uuid IS NULL OR model.id = sqlc.narg(configured_model_id)::uuid)
  AND (COALESCE(cardinality(sqlc.arg(api_formats)::text[]), 0) = 0 OR provider.api_format = ANY(sqlc.arg(api_formats)::text[]))
  AND (COALESCE(cardinality(sqlc.arg(api_variants)::text[]), 0) = 0 OR provider.api_variant = ANY(sqlc.arg(api_variants)::text[]))
)
SELECT id, org_id, project_id, name, current_config_id, current_generation,
       idempotency_key, created_at, updated_at, config_id, config_org_id,
       config_project_id, config_configured_model_id, config_source,
       config_source_format, config_source_hash, config_compiled_definition,
       config_compiler_version, config_effective_definition_hash,
       config_created_at, sort_key, sort_is_null
FROM listed
WHERE sqlc.arg(cursor_set)::boolean = false
   OR (sqlc.arg(sort_desc)::boolean = false AND (sort_key, id) > (sqlc.arg(cursor_key)::text, sqlc.arg(cursor_id)::uuid))
   OR (sqlc.arg(sort_desc)::boolean = true AND (sort_key, id) < (sqlc.arg(cursor_key)::text, sqlc.arg(cursor_id)::uuid))
ORDER BY CASE WHEN sqlc.arg(sort_desc)::boolean = false THEN sort_key END ASC,
         CASE WHEN sqlc.arg(sort_desc)::boolean = true THEN sort_key END DESC,
         CASE WHEN sqlc.arg(sort_desc)::boolean = false THEN id END ASC,
         CASE WHEN sqlc.arg(sort_desc)::boolean = true THEN id END DESC
LIMIT sqlc.arg(row_limit)::bigint;

-- name: ListRecentAgentProfilesForProjects :many
SELECT profile.id, project.org_id AS org_id, profile.project_id, profile.name,
       version.agent_config_id AS current_config_id,
       version.generation AS current_generation,
       coalesce(profile.idempotency_key, '') AS idempotency_key,
       profile.created_at, profile.updated_at,
       config.id AS config_id, config.org_id AS config_org_id,
       config.project_id AS config_project_id,
       config.configured_model_id AS config_configured_model_id,
       config.source AS config_source, config.source_format AS config_source_format,
       config.source_hash AS config_source_hash,
       config.compiled_definition AS config_compiled_definition,
       config.compiler_version AS config_compiler_version,
       config.effective_definition_hash AS config_effective_definition_hash,
       config.created_at AS config_created_at
FROM agent_profiles profile
JOIN projects project ON project.id = profile.project_id
JOIN agent_profile_versions version
  ON version.project_id = profile.project_id
 AND version.profile_id = profile.id
 AND version.id = profile.current_version_id
 AND version.deleted_at IS NULL
JOIN agent_configs config ON config.project_id = profile.project_id
  AND config.id = version.agent_config_id
WHERE profile.project_id = ANY(sqlc.arg(project_ids)::uuid[])
  AND profile.deleted_at IS NULL
ORDER BY profile.updated_at DESC, profile.id DESC
LIMIT sqlc.arg(row_limit)::bigint;

-- name: LockAgentProfile :one
SELECT profile.id
FROM agent_profiles profile
WHERE profile.project_id = sqlc.arg(project_id)
  AND profile.id = sqlc.arg(profile_id)
  AND profile.deleted_at IS NULL
FOR UPDATE;

-- name: GetAgentProfileByIdempotencyKey :one
SELECT profile.id, project.org_id, profile.project_id, profile.name,
       version.agent_config_id AS current_config_id,
       version.generation AS current_generation,
       coalesce(profile.idempotency_key, '') AS idempotency_key, profile.created_at, profile.updated_at
FROM agent_profiles profile
JOIN projects project ON project.id = profile.project_id
JOIN agent_profile_versions version
  ON version.project_id = profile.project_id
 AND version.profile_id = profile.id
 AND version.id = profile.current_version_id
 AND version.deleted_at IS NULL
WHERE profile.project_id = sqlc.arg(project_id)
  AND profile.idempotency_key = sqlc.arg(idempotency_key)::text
  AND profile.deleted_at IS NULL;

-- name: GetAgentProfileVersionByGeneration :one
SELECT version.id, project.org_id, version.project_id, version.profile_id, version.generation, version.agent_config_id,
       version.reason, coalesce(version.idempotency_key, '') AS idempotency_key,
       version.created_at
FROM agent_profile_versions version
JOIN projects project ON project.id = version.project_id
WHERE version.project_id = sqlc.arg(project_id)
  AND version.profile_id = sqlc.arg(profile_id)
  AND version.deleted_at IS NULL
  AND version.generation = sqlc.arg(generation)::integer;

-- name: GetAgentProfileVersionByIdempotencyKey :one
SELECT version.id, project.org_id, version.project_id, version.profile_id, version.generation, version.agent_config_id,
       version.reason, coalesce(version.idempotency_key, '') AS idempotency_key,
       version.created_at
FROM agent_profile_versions version
JOIN projects project ON project.id = version.project_id
WHERE version.project_id = sqlc.arg(project_id)
  AND version.profile_id = sqlc.arg(profile_id)
  AND version.deleted_at IS NULL
  AND version.idempotency_key = sqlc.arg(idempotency_key)::text;

-- name: RetargetAgentProfile :one
WITH current_profile AS MATERIALIZED (
    SELECT profile.id, profile.project_id, profile.name,
           profile.idempotency_key, profile.created_at,
           version.generation
    FROM agent_profiles profile
    JOIN agent_profile_versions version
      ON version.project_id = profile.project_id
     AND version.profile_id = profile.id
     AND version.id = profile.current_version_id
     AND version.deleted_at IS NULL
    WHERE profile.project_id = sqlc.arg(project_id)
      AND profile.id = sqlc.arg(profile_id)
      AND profile.deleted_at IS NULL
      AND version.agent_config_id = sqlc.arg(expected_current_config_id)
    FOR UPDATE OF profile
), new_version AS (
    INSERT INTO agent_profile_versions(
        project_id, profile_id, generation, agent_config_id,
        reason, idempotency_key, created_at
    )
    SELECT profile.project_id, profile.id, profile.generation + 1,
           sqlc.arg(current_config_id), sqlc.arg(reason),
           sqlc.narg(idempotency_key), statement_timestamp()
    FROM current_profile profile
    RETURNING id, project_id, profile_id, generation, agent_config_id
), updated_profile AS (
    UPDATE agent_profiles profile
    SET current_version_id = version.id,
        updated_at = statement_timestamp()
    FROM new_version version
    WHERE profile.project_id = version.project_id
      AND profile.id = version.profile_id
    RETURNING profile.id, profile.project_id, profile.name,
              profile.idempotency_key, profile.created_at,
              profile.updated_at, profile.current_version_id
)
SELECT profile.id, profile.project_id, profile.name,
       version.agent_config_id AS current_config_id,
       version.generation AS current_generation,
       coalesce(profile.idempotency_key, '') AS idempotency_key,
       profile.created_at, profile.updated_at
FROM updated_profile profile
JOIN new_version version
  ON version.project_id = profile.project_id
 AND version.profile_id = profile.id
 AND version.id = profile.current_version_id;

-- name: RenameAgentProfile :execrows
UPDATE agent_profiles
SET name = sqlc.arg(name),
    updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(profile_id)
  AND deleted_at IS NULL;

-- name: DeleteAgentProfile :execrows
UPDATE agent_profiles
SET deleted_at = statement_timestamp(),
    updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(profile_id)
  AND deleted_at IS NULL;

-- name: DeleteAgentProfileVersions :exec
UPDATE agent_profile_versions
SET deleted_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id)
  AND profile_id = sqlc.arg(profile_id)
  AND deleted_at IS NULL;

-- name: AgentProfileHasIntegrationInstall :one
SELECT EXISTS (
  SELECT 1 FROM integration_installs
  WHERE project_id = sqlc.arg(project_id)
    AND agent_profile_id = sqlc.arg(profile_id)
    AND state = 'active'
    AND deleted_at IS NULL
) AS has_integration_install;

-- name: AgentProfileVersionExistsForConfig :one
-- @sqlc-vet-disable agent-profile-versions-deleted-at
-- Config lineage check spans soft-deleted profile versions.
SELECT EXISTS (
  SELECT 1
FROM agent_profile_versions
WHERE project_id = sqlc.arg(project_id)
  AND profile_id = sqlc.arg(profile_id)
  AND agent_config_id = sqlc.arg(agent_config_id)
);
