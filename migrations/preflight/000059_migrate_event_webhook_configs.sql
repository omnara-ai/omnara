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

ROLLBACK;
