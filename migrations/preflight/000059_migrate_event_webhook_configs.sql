BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;

DO $preflight$
BEGIN
    IF pg_is_in_recovery() OR
       coalesce((SELECT max(version_id) FILTER (WHERE is_applied) FROM goose_db_version), 0)
           NOT IN (57, 58) THEN
        RAISE EXCEPTION 'preflight requires a schema-57 or schema-58 writer';
    END IF;
END;
$preflight$;

WITH legacy_event_webhook_configs AS (
    SELECT id, compiled_definition->'event_webhook'->>'url' AS url,
           ARRAY(SELECT DISTINCT jsonb_array_elements_text(compiled_definition->'event_webhook'->'events')
                 ORDER BY 1) AS events,
           NULLIF(compiled_definition->'event_webhook'->>'signing_secret_id',
                  '00000000-0000-0000-0000-000000000000')::uuid AS signing_secret_id
    FROM agent_configs WHERE jsonb_typeof(compiled_definition->'event_webhook') = 'object'
)
SELECT agent.org_id, agent.project_id, agent.id AS agent_id, profile.id AS agent_profile_id,
       agent.state, own.id IS NULL AS agent_has_no_webhook, pending.unmapped_deliveries
FROM agents agent
JOIN agent_profiles profile ON profile.id = agent.agent_profile_id AND profile.deleted_at IS NULL
JOIN agent_profile_versions version ON version.id = profile.current_version_id
JOIN legacy_event_webhook_configs inherited ON inherited.id = version.agent_config_id
LEFT JOIN legacy_event_webhook_configs own ON own.id = agent.current_config_id
JOIN projects project ON project.id = agent.project_id AND project.deleted_at IS NULL
JOIN orgs org ON org.id = project.org_id AND org.deleted_at IS NULL
CROSS JOIN LATERAL (
    SELECT count(*) AS unmapped_deliveries FROM event_webhook_deliveries delivery
    WHERE delivery.agent_id = agent.id AND to_jsonb(delivery)->>'webhook_id' IS NULL
) pending
WHERE ROW(inherited.url, inherited.events, inherited.signing_secret_id)
      IS DISTINCT FROM ROW(own.url, own.events, own.signing_secret_id)
  AND (agent.state = 'active' OR pending.unmapped_deliveries > 0)
ORDER BY agent.org_id, agent.project_id, agent.id;

WITH scoped_configs AS (
    SELECT profile.project_id, NULL::uuid AS agent_id, profile.id AS agent_profile_id,
           version.agent_config_id AS config_id, NULL::text AS agent_state
    FROM agent_profiles profile
    JOIN agent_profile_versions version ON version.id = profile.current_version_id
    WHERE profile.deleted_at IS NULL
    UNION ALL
    SELECT agent.project_id, agent.id, NULL::uuid, agent.current_config_id, agent.state
    FROM agents agent
    WHERE NOT EXISTS (
        SELECT 1 FROM agent_profiles profile
        JOIN agent_profile_versions version ON version.id = profile.current_version_id
        JOIN agent_configs config ON config.id = version.agent_config_id
        WHERE profile.id = agent.agent_profile_id AND profile.deleted_at IS NULL
          AND jsonb_typeof(config.compiled_definition->'event_webhook') = 'object'
    )
      AND (agent.state = 'active' OR EXISTS (
          SELECT 1 FROM event_webhook_deliveries delivery
          WHERE delivery.agent_id = agent.id AND to_jsonb(delivery)->>'webhook_id' IS NULL
      ))
)
SELECT project.org_id, scope.project_id, scope.agent_id, scope.agent_profile_id, scope.config_id,
       octet_length(config.compiled_definition->'event_webhook'->>'url') AS url_bytes, scope.agent_state,
       CASE WHEN scope.agent_state = 'archived'
           THEN 'Let existing workers deliver this archived agent''s pending webhooks or maintenance remove expired deliveries, then rerun the preflight. Never truncate URLs or manually discard deliveries to bypass this check.'
           ELSE 'Configure a shorter URL serving the same receiver and update the affected configs through the API; preserve events and signing settings, then rerun. Never truncate a URL; defer upgrading if no equivalent endpoint is available.'
       END AS resolution
FROM scoped_configs scope
JOIN agent_configs config ON config.id = scope.config_id
JOIN projects project ON project.id = scope.project_id AND project.deleted_at IS NULL
JOIN orgs org ON org.id = project.org_id AND org.deleted_at IS NULL
WHERE octet_length(config.compiled_definition->'event_webhook'->>'url') > 2048
ORDER BY project.org_id, scope.project_id, scope.agent_id, scope.agent_profile_id;

WITH legacy_event_webhook_configs AS (
    SELECT id, compiled_definition->'event_webhook'->>'url' AS url,
           ARRAY(SELECT DISTINCT jsonb_array_elements_text(compiled_definition->'event_webhook'->'events')
                 ORDER BY 1) AS events,
           NULLIF(compiled_definition->'event_webhook'->>'signing_secret_id',
                  '00000000-0000-0000-0000-000000000000')::uuid AS signing_secret_id
    FROM agent_configs WHERE jsonb_typeof(compiled_definition->'event_webhook') = 'object'
), self_spawns AS (
    SELECT agent.id AS agent_id, config.id AS config_id FROM agents agent
    JOIN agent_configs config ON config.id = agent.current_config_id
    WHERE config.compiled_definition#>>'{tools,spawn_agent,enabled}' = 'true'
      AND EXISTS (
          SELECT 1 FROM jsonb_each(coalesce(nullif(config.compiled_definition->'subagents', 'null'::jsonb), '{}')) subagent
          WHERE subagent.value->>'type' = 'self'
      )
    UNION
    SELECT call.agent_id, context.agent_config_id FROM tool_calls call
    JOIN model_outputs output ON output.agent_id = call.agent_id AND output.id = call.model_output_id
    JOIN model_call_contexts context ON context.agent_id = output.agent_id AND context.id = output.model_call_context_id
    JOIN agent_configs config ON config.id = context.agent_config_id
    WHERE call.type = 'built_in' AND call.name = 'spawn_agent' AND call.state <> 'completed'
      AND config.compiled_definition#>>'{tools,spawn_agent,enabled}' = 'true'
      AND config.compiled_definition->'subagents'->btrim(call.input->>'agent',
          U&'\0009\000A\000B\000C\000D\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000')->>'type' = 'self'
)
SELECT agent.org_id, agent.project_id, agent.id AS agent_id, agent.agent_profile_id, own.id AS config_id,
       'Finish its work, relaunch under a profile with the same webhook URL, events, and signing secret, archive the original, then rerun the preflight.' AS resolution
FROM self_spawns spawn
JOIN agents agent ON agent.id = spawn.agent_id
JOIN legacy_event_webhook_configs own ON own.id = spawn.config_id
JOIN projects project ON project.id = agent.project_id AND project.deleted_at IS NULL
JOIN orgs org ON org.id = project.org_id AND org.deleted_at IS NULL
WHERE agent.state = 'active'
  AND NOT EXISTS (
      SELECT 1 FROM agent_profiles profile
      JOIN agent_profile_versions version ON version.id = profile.current_version_id
      JOIN legacy_event_webhook_configs inherited ON inherited.id = version.agent_config_id
      WHERE profile.id = agent.agent_profile_id AND profile.deleted_at IS NULL
        AND ROW(inherited.url, inherited.events, inherited.signing_secret_id)
            IS NOT DISTINCT FROM ROW(own.url, own.events, own.signing_secret_id)
  )
ORDER BY agent.org_id, agent.project_id, agent.id, own.id;

ROLLBACK;
