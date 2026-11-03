-- name: SumModelCallUsageByModel :many
-- @sqlc-vet-disable model-provider-configs-deleted-at configured-models-deleted-at
-- Usage must still resolve when the configured model or provider config is soft deleted.
WITH RECURSIVE profile_agents AS (
  SELECT agent.id, 1 AS depth
  FROM agents agent
  WHERE sqlc.narg(agent_profile_id)::uuid IS NOT NULL
    AND agent.project_id = sqlc.narg(project_id)::uuid
    AND agent.agent_profile_id = sqlc.narg(agent_profile_id)::uuid
    AND agent.parent_agent_id IS NULL
  UNION ALL
  SELECT child.id, profile_agents.depth + 1
  FROM agents child
  JOIN profile_agents ON child.parent_agent_id = profile_agents.id
  WHERE sqlc.arg(include_profile_subagents)::boolean
    AND child.project_id = sqlc.narg(project_id)::uuid
    AND profile_agents.depth < 64
)
SELECT revision.configured_model_id,
       configured_model.name AS configured_model_name,
       revision.provider_model_slug,
       provider_config.id AS model_provider_config_id,
       provider_config.name AS model_provider_config_name,
       count(*)::bigint AS model_calls,
       count(context.provider_reported_cost_usd)::bigint AS model_calls_with_reported_cost,
       coalesce(sum(context.input_tokens_total), 0)::bigint AS input_tokens_total,
       coalesce(sum(context.uncached_input_tokens), 0)::bigint AS uncached_input_tokens,
       coalesce(sum(context.cache_read_input_tokens), 0)::bigint AS cache_read_input_tokens,
       coalesce(sum(context.cache_write_input_tokens), 0)::bigint AS cache_write_input_tokens,
       coalesce(sum(context.output_tokens_total), 0)::bigint AS output_tokens_total,
       coalesce(sum(context.reasoning_output_tokens), 0)::bigint AS reasoning_output_tokens,
       coalesce(sum(context.provider_reported_cost_usd), 0)::text AS provider_reported_cost_usd
FROM model_call_contexts context
JOIN configured_model_revisions revision ON revision.org_id = context.org_id
  AND revision.id = context.configured_model_revision_id
JOIN configured_models configured_model ON configured_model.org_id = revision.org_id
  AND configured_model.id = revision.configured_model_id
JOIN model_provider_configs provider_config ON provider_config.org_id = revision.org_id
  AND provider_config.id = revision.model_provider_config_id
WHERE context.org_id = sqlc.arg(org_id)
  AND (sqlc.narg(project_id)::uuid IS NULL OR context.project_id = sqlc.narg(project_id)::uuid)
  AND (
    sqlc.narg(include_project_ids)::uuid[] IS NULL
    OR context.project_id = ANY(sqlc.narg(include_project_ids)::uuid[])
  )
  AND (
    sqlc.narg(exclude_project_ids)::uuid[] IS NULL
    OR context.project_id <> ALL(sqlc.narg(exclude_project_ids)::uuid[])
  )
  AND (
    sqlc.narg(agent_profile_id)::uuid IS NULL
    OR context.agent_id IN (SELECT profile_agents.id FROM profile_agents)
  )
  AND (sqlc.narg(agent_ids)::uuid[] IS NULL OR context.agent_id = ANY(sqlc.narg(agent_ids)::uuid[]))
  AND (sqlc.narg(since)::timestamptz IS NULL OR context.created_at >= sqlc.narg(since)::timestamptz)
  AND (sqlc.narg(until)::timestamptz IS NULL OR context.created_at < sqlc.narg(until)::timestamptz)
  AND (
    context.input_tokens_total IS NOT NULL
    OR context.output_tokens_total IS NOT NULL
    OR context.provider_reported_cost_usd IS NOT NULL
  )
GROUP BY revision.configured_model_id, configured_model.name, revision.provider_model_slug,
         provider_config.id, provider_config.name
ORDER BY provider_config.name, configured_model.name, revision.provider_model_slug;

-- name: SumModelCallUsageByBucket :many
-- @sqlc-vet-disable configured-models-deleted-at agent-profiles-deleted-at model-provider-configs-deleted-at projects-deleted-at
-- Usage must still resolve when its model, provider config, profile, or project is soft deleted.
-- Provider and project names tell apart same-named models and profiles.
WITH RECURSIVE profile_agents AS (
  SELECT agent.id, 1 AS depth
  FROM agents agent
  WHERE sqlc.narg(agent_profile_ids)::uuid[] IS NOT NULL
    AND (sqlc.narg(project_ids)::uuid[] IS NULL OR agent.project_id = ANY(sqlc.narg(project_ids)::uuid[]))
    AND agent.agent_profile_id = ANY(sqlc.narg(agent_profile_ids)::uuid[])
    AND agent.parent_agent_id IS NULL
  UNION ALL
  SELECT child.id, profile_agents.depth + 1
  FROM agents child
  JOIN profile_agents ON child.parent_agent_id = profile_agents.id
  WHERE sqlc.arg(include_profile_subagents)::boolean
    AND (sqlc.narg(project_ids)::uuid[] IS NULL OR child.project_id = ANY(sqlc.narg(project_ids)::uuid[]))
    AND profile_agents.depth < 64
)
SELECT width_bucket(context.created_at, sqlc.arg(bucket_starts)::timestamptz[])::integer AS bucket_number,
       revision.configured_model_id,
       configured_model.name AS configured_model_name,
       provider_config.name AS model_provider_config_name,
       agent.agent_profile_id,
       coalesce(profile.name, '')::text AS agent_profile_name,
       project.name AS project_name,
       count(*)::bigint AS model_calls,
       count(context.provider_reported_cost_usd)::bigint AS model_calls_with_reported_cost,
       coalesce(sum(context.input_tokens_total), 0)::bigint AS input_tokens_total,
       coalesce(sum(context.uncached_input_tokens), 0)::bigint AS uncached_input_tokens,
       coalesce(sum(context.cache_read_input_tokens), 0)::bigint AS cache_read_input_tokens,
       coalesce(sum(context.cache_write_input_tokens), 0)::bigint AS cache_write_input_tokens,
       coalesce(sum(context.output_tokens_total), 0)::bigint AS output_tokens_total,
       coalesce(sum(context.reasoning_output_tokens), 0)::bigint AS reasoning_output_tokens,
       coalesce(sum(context.provider_reported_cost_usd), 0)::text AS provider_reported_cost_usd
FROM model_call_contexts context
JOIN configured_model_revisions revision ON revision.org_id = context.org_id
  AND revision.id = context.configured_model_revision_id
JOIN configured_models configured_model ON configured_model.org_id = revision.org_id
  AND configured_model.id = revision.configured_model_id
JOIN model_provider_configs provider_config ON provider_config.org_id = configured_model.org_id
  AND provider_config.id = configured_model.model_provider_config_id
JOIN projects project ON project.org_id = context.org_id
  AND project.id = context.project_id
JOIN agents agent ON agent.project_id = context.project_id
  AND agent.id = context.agent_id
LEFT JOIN agent_profiles profile ON profile.project_id = agent.project_id
  AND profile.id = agent.agent_profile_id
WHERE context.org_id = ANY(sqlc.arg(org_ids)::uuid[])
  AND (sqlc.narg(project_ids)::uuid[] IS NULL OR context.project_id = ANY(sqlc.narg(project_ids)::uuid[]))
  AND (sqlc.narg(since)::timestamptz IS NULL OR context.created_at >= sqlc.narg(since)::timestamptz)
  AND context.created_at < sqlc.arg(until)::timestamptz
  AND (
    sqlc.narg(agent_profile_ids)::uuid[] IS NULL
    OR context.agent_id IN (SELECT profile_agents.id FROM profile_agents)
  )
  AND (
    context.input_tokens_total IS NOT NULL
    OR context.output_tokens_total IS NOT NULL
    OR context.provider_reported_cost_usd IS NOT NULL
  )
GROUP BY bucket_number, revision.configured_model_id, configured_model.name, provider_config.name,
         agent.agent_profile_id, profile.name, project.id, project.name
ORDER BY bucket_number, revision.configured_model_id, agent.agent_profile_id;

-- name: FirstModelCallUsageAt :one
WITH RECURSIVE profile_agents AS (
  SELECT agent.id, 1 AS depth
  FROM agents agent
  WHERE sqlc.narg(agent_profile_ids)::uuid[] IS NOT NULL
    AND (sqlc.narg(project_ids)::uuid[] IS NULL OR agent.project_id = ANY(sqlc.narg(project_ids)::uuid[]))
    AND agent.agent_profile_id = ANY(sqlc.narg(agent_profile_ids)::uuid[])
    AND agent.parent_agent_id IS NULL
  UNION ALL
  SELECT child.id, profile_agents.depth + 1
  FROM agents child
  JOIN profile_agents ON child.parent_agent_id = profile_agents.id
  WHERE sqlc.arg(include_profile_subagents)::boolean
    AND (sqlc.narg(project_ids)::uuid[] IS NULL OR child.project_id = ANY(sqlc.narg(project_ids)::uuid[]))
    AND profile_agents.depth < 64
)
SELECT context.created_at
FROM model_call_contexts context
WHERE context.org_id = ANY(sqlc.arg(org_ids)::uuid[])
  AND (sqlc.narg(project_ids)::uuid[] IS NULL OR context.project_id = ANY(sqlc.narg(project_ids)::uuid[]))
  AND (sqlc.narg(since)::timestamptz IS NULL OR context.created_at >= sqlc.narg(since)::timestamptz)
  AND context.created_at < sqlc.arg(until)::timestamptz
  AND (
    sqlc.narg(agent_profile_ids)::uuid[] IS NULL
    OR context.agent_id IN (SELECT profile_agents.id FROM profile_agents)
  )
  AND (
    context.input_tokens_total IS NOT NULL
    OR context.output_tokens_total IS NOT NULL
    OR context.provider_reported_cost_usd IS NOT NULL
  )
ORDER BY context.created_at
LIMIT 1;

-- name: CountAgentsWithModelCalls :one
WITH RECURSIVE profile_agents AS (
  SELECT agent.id, 1 AS depth
  FROM agents agent
  WHERE sqlc.narg(agent_profile_ids)::uuid[] IS NOT NULL
    AND (sqlc.narg(project_ids)::uuid[] IS NULL OR agent.project_id = ANY(sqlc.narg(project_ids)::uuid[]))
    AND agent.agent_profile_id = ANY(sqlc.narg(agent_profile_ids)::uuid[])
    AND agent.parent_agent_id IS NULL
  UNION ALL
  SELECT child.id, profile_agents.depth + 1
  FROM agents child
  JOIN profile_agents ON child.parent_agent_id = profile_agents.id
  WHERE sqlc.arg(include_profile_subagents)::boolean
    AND (sqlc.narg(project_ids)::uuid[] IS NULL OR child.project_id = ANY(sqlc.narg(project_ids)::uuid[]))
    AND profile_agents.depth < 64
)
SELECT count(DISTINCT context.agent_id)::bigint AS agent_count
FROM model_call_contexts context
JOIN agents agent ON agent.project_id = context.project_id
  AND agent.id = context.agent_id
WHERE agent.parent_agent_id IS NULL
  AND context.org_id = ANY(sqlc.arg(org_ids)::uuid[])
  AND (sqlc.narg(project_ids)::uuid[] IS NULL OR context.project_id = ANY(sqlc.narg(project_ids)::uuid[]))
  AND (sqlc.narg(since)::timestamptz IS NULL OR context.created_at >= sqlc.narg(since)::timestamptz)
  AND context.created_at < sqlc.arg(until)::timestamptz
  AND (
    sqlc.narg(agent_profile_ids)::uuid[] IS NULL
    OR context.agent_id IN (SELECT profile_agents.id FROM profile_agents)
  )
  AND (
    context.input_tokens_total IS NOT NULL
    OR context.output_tokens_total IS NOT NULL
    OR context.provider_reported_cost_usd IS NOT NULL
  );

-- name: SumModelCallUsageByAgentProfile :many
-- Each profile's usage by its top-level agents and their subagents, for a page of profiles at once.
WITH RECURSIVE profile_agents AS (
  SELECT agent.id, agent.agent_profile_id AS profile_id, 1 AS depth
  FROM agents agent
  WHERE agent.project_id = ANY(sqlc.arg(project_ids)::uuid[])
    AND agent.agent_profile_id = ANY(sqlc.arg(agent_profile_ids)::uuid[])
    AND agent.parent_agent_id IS NULL
  UNION ALL
  SELECT child.id, profile_agents.profile_id, profile_agents.depth + 1
  FROM agents child
  JOIN profile_agents ON child.parent_agent_id = profile_agents.id
  WHERE child.project_id = ANY(sqlc.arg(project_ids)::uuid[])
    AND profile_agents.depth < 64
)
SELECT profile_agents.profile_id::uuid AS agent_profile_id,
       count(*)::bigint AS model_calls,
       count(context.provider_reported_cost_usd)::bigint AS model_calls_with_reported_cost,
       coalesce(sum(context.input_tokens_total), 0)::bigint AS input_tokens_total,
       coalesce(sum(context.uncached_input_tokens), 0)::bigint AS uncached_input_tokens,
       coalesce(sum(context.cache_read_input_tokens), 0)::bigint AS cache_read_input_tokens,
       coalesce(sum(context.cache_write_input_tokens), 0)::bigint AS cache_write_input_tokens,
       coalesce(sum(context.output_tokens_total), 0)::bigint AS output_tokens_total,
       coalesce(sum(context.reasoning_output_tokens), 0)::bigint AS reasoning_output_tokens,
       coalesce(sum(context.provider_reported_cost_usd), 0)::text AS provider_reported_cost_usd
FROM model_call_contexts context
JOIN profile_agents ON profile_agents.id = context.agent_id
WHERE context.project_id = ANY(sqlc.arg(project_ids)::uuid[])
  AND (sqlc.narg(since)::timestamptz IS NULL OR context.created_at >= sqlc.narg(since)::timestamptz)
  AND (
    context.input_tokens_total IS NOT NULL
    OR context.output_tokens_total IS NOT NULL
    OR context.provider_reported_cost_usd IS NOT NULL
  )
GROUP BY profile_agents.profile_id;

-- name: SumModelCallUsageByAgentTree :many
-- Each agent's usage by itself and its subagents, for a page of agents at once.
WITH RECURSIVE agent_trees AS (
  SELECT agent.id, agent.id AS root_agent_id, 1 AS depth
  FROM agents agent
  WHERE agent.project_id = ANY(sqlc.arg(project_ids)::uuid[])
    AND agent.id = ANY(sqlc.arg(agent_ids)::uuid[])
  UNION ALL
  SELECT child.id, agent_trees.root_agent_id, agent_trees.depth + 1
  FROM agents child
  JOIN agent_trees ON child.parent_agent_id = agent_trees.id
  WHERE child.project_id = ANY(sqlc.arg(project_ids)::uuid[])
    AND agent_trees.depth < 64
)
SELECT agent_trees.root_agent_id::uuid AS agent_id,
       count(*)::bigint AS model_calls,
       count(context.provider_reported_cost_usd)::bigint AS model_calls_with_reported_cost,
       coalesce(sum(context.input_tokens_total), 0)::bigint AS input_tokens_total,
       coalesce(sum(context.uncached_input_tokens), 0)::bigint AS uncached_input_tokens,
       coalesce(sum(context.cache_read_input_tokens), 0)::bigint AS cache_read_input_tokens,
       coalesce(sum(context.cache_write_input_tokens), 0)::bigint AS cache_write_input_tokens,
       coalesce(sum(context.output_tokens_total), 0)::bigint AS output_tokens_total,
       coalesce(sum(context.reasoning_output_tokens), 0)::bigint AS reasoning_output_tokens,
       coalesce(sum(context.provider_reported_cost_usd), 0)::text AS provider_reported_cost_usd
FROM model_call_contexts context
JOIN agent_trees ON agent_trees.id = context.agent_id
WHERE context.project_id = ANY(sqlc.arg(project_ids)::uuid[])
  AND (
    context.input_tokens_total IS NOT NULL
    OR context.output_tokens_total IS NOT NULL
    OR context.provider_reported_cost_usd IS NOT NULL
  )
GROUP BY agent_trees.root_agent_id;
