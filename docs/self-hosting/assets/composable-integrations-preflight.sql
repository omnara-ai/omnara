-- Schema-44 inventory of the gates in migrations/000045_composable_integrations.sql.
-- Run with psql -X -v ON_ERROR_STOP=1. Every blocker result must be empty.
BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;

SELECT current_database(), pg_is_in_recovery(),
       max(version_id) FILTER (WHERE is_applied) AS applied_version
FROM goose_db_version;

DO $preflight$
BEGIN
    IF pg_is_in_recovery() OR
       (SELECT max(version_id) FILTER (WHERE is_applied) FROM goose_db_version)
           IS DISTINCT FROM 44 THEN
        RAISE EXCEPTION 'preflight requires the schema-44 writer';
    END IF;
END;
$preflight$;

-- Raw work is deployment-wide, including agents without a Slack target.
WITH raw_work AS (
SELECT 'runtime_lock' AS blocker, id, agent_id
FROM agent_runtime_locks WHERE lease_expires_at > statement_timestamp()
UNION ALL
SELECT 'started_model_context', id, agent_id
FROM model_call_contexts WHERE state = 'started'
UNION ALL
SELECT 'unfinished_tool_call', id, agent_id
FROM tool_calls WHERE state <> 'completed'
UNION ALL
SELECT 'open_interaction', id, agent_id
FROM agent_interactions WHERE state = 'open'
)
SELECT raw_work.blocker, agent.org_id, agent.project_id, raw_work.id, raw_work.agent_id
FROM raw_work
JOIN agents agent ON agent.id = raw_work.agent_id
ORDER BY raw_work.blocker, raw_work.agent_id, raw_work.id;

SELECT agent.org_id, agent.project_id, agent.id AS agent_id, latest.id AS turn_id
FROM agents agent
JOIN LATERAL (
    SELECT id FROM agent_turns WHERE agent_id = agent.id
    ORDER BY turn_sequence DESC LIMIT 1
) latest ON true
WHERE EXISTS (
        SELECT 1 FROM agent_continuable_model_contexts(agent.project_id, agent.id) context
        WHERE context.turn_id = latest.id)
   OR agent_has_incomplete_tool_batch(agent.project_id, agent.id)
   OR EXISTS (
        SELECT 1 FROM agent_next_model_work(agent.project_id, agent.id) frontier
        WHERE frontier.turn_id = latest.id)
ORDER BY agent.project_id, agent.id;

SELECT config.project_id, config.id AS config_id, tool.key AS conflicting_tool
FROM agent_configs config
CROSS JOIN LATERAL jsonb_each(
    coalesce(nullif(config.compiled_definition->'tools', 'null'::jsonb), '{}'::jsonb)
) tool
WHERE tool.value->>'type' = 'custom'
  AND (starts_with(tool.key, 'int__')
       OR tool.key IN ('list_interaction_handlers', 'set_interaction_handler'))
ORDER BY config.project_id, config.id, tool.key;

SELECT project_id, id AS install_id
FROM integration_installs
WHERE provider <> 'slack' OR agent_id IS NOT NULL OR agent_profile_id IS NULL
   OR connection_mode <> 'webhook'
   OR (state = 'active' AND deleted_at IS NULL AND credential_secret_id IS NULL)
ORDER BY project_id, id;

SELECT config.project_id, config.id AS config_id
FROM agent_configs config
CROSS JOIN LATERAL (
    SELECT config.compiled_definition->'tools'->'send_integration_message' AS value
) policy
WHERE policy.value IS NOT NULL AND (
    jsonb_typeof(policy.value) <> 'object'
    OR policy.value - ARRAY['enabled','deferred','permission']::text[] <> '{}'::jsonb
    OR (policy.value ? 'enabled' AND jsonb_typeof(policy.value->'enabled') <> 'boolean')
    OR (policy.value ? 'deferred' AND jsonb_typeof(policy.value->'deferred') <> 'boolean')
    OR (policy.value ? 'permission' AND (
        jsonb_typeof(policy.value->'permission') <> 'object'
        OR (policy.value->'permission'->>'mode') IS DISTINCT FROM 'always_allow'
        OR (policy.value->'permission') - ARRAY['mode','parameters']::text[] <> '{}'::jsonb
        OR (policy.value->'permission' ? 'parameters'
            AND policy.value->'permission'->'parameters' <> '{}'::jsonb)))
    OR (policy.value->'enabled' = 'false'::jsonb AND NOT EXISTS (
        SELECT 1 FROM integration_installs install WHERE install.project_id = config.project_id)))
ORDER BY config.project_id, config.id;

SELECT target.agent_id, target.integration_install_id,
       array_agg(target.id ORDER BY target.id) AS target_ids, count(*) AS target_count
FROM integration_targets target
JOIN integration_installs install ON install.id = target.integration_install_id
JOIN agents agent ON agent.project_id = target.project_id AND agent.id = target.agent_id
JOIN projects project ON project.id = target.project_id
JOIN orgs org ON org.id = project.org_id
WHERE target.deleted_at IS NULL AND install.deleted_at IS NULL
  AND project.deleted_at IS NULL AND org.deleted_at IS NULL
GROUP BY target.agent_id, target.integration_install_id
HAVING count(*) > 1
ORDER BY target.agent_id, target.integration_install_id;

SELECT target.project_id, target.agent_id, target.id AS target_id
FROM integration_targets target
JOIN integration_installs install ON install.id = target.integration_install_id
    AND install.deleted_at IS NULL
JOIN agents agent ON agent.project_id = target.project_id AND agent.id = target.agent_id
JOIN projects project ON project.id = target.project_id AND project.deleted_at IS NULL
JOIN orgs org ON org.id = project.org_id AND org.deleted_at IS NULL
WHERE target.deleted_at IS NULL AND NOT CASE target.provider_ref_kind
    WHEN 'dm' THEN target.provider_ref COLLATE "C" ~ '^[CDG][A-Z0-9]+$'
    WHEN 'channel' THEN target.provider_ref COLLATE "C" ~ '^[CDG][A-Z0-9]+$'
    WHEN 'thread' THEN target.provider_ref COLLATE "C" ~ '^[CDG][A-Z0-9]+:[0-9]+[.][0-9]+$'
    ELSE false END
ORDER BY target.project_id, target.agent_id, target.id;

WITH successors AS (
    SELECT agent.project_id, count(DISTINCT agent.id) AS needed
    FROM agents agent
    JOIN projects project ON project.id = agent.project_id AND project.deleted_at IS NULL
    JOIN orgs org ON org.id = project.org_id AND org.deleted_at IS NULL
    JOIN integration_targets target ON target.project_id = agent.project_id AND target.agent_id = agent.id
        AND target.deleted_at IS NULL
    JOIN integration_installs install ON install.id = target.integration_install_id AND install.deleted_at IS NULL
    GROUP BY agent.project_id
), config_counts AS (
    SELECT project_id, count(*) AS existing FROM agent_configs GROUP BY project_id
)
SELECT project.id AS project_id, configs.existing, successors.needed,
       coalesce(limits.max_agent_configs_per_project, 10000000) AS config_limit,
       configs.existing + successors.needed
           - coalesce(limits.max_agent_configs_per_project, 10000000) AS missing_capacity
FROM successors
JOIN projects project ON project.id = successors.project_id
JOIN config_counts configs ON configs.project_id = project.id
LEFT JOIN org_resource_limit_overrides limits ON limits.org_id = project.org_id
WHERE configs.existing + successors.needed > coalesce(limits.max_agent_configs_per_project, 10000000)
ORDER BY project.id;

ROLLBACK;
