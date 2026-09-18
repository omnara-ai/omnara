-- name: GetAgentEventWebhookTarget :one
SELECT agent.project_id, project.org_id,
       coalesce(config.compiled_definition->'event_webhook'->>'url', '')::text AS url,
       coalesce(config.compiled_definition->'event_webhook'->>'signing_secret_id', '')::text AS signing_secret_id,
       coalesce(config.compiled_definition->'event_webhook'->'events', 'null'::jsonb)::jsonb AS events
FROM agents agent
JOIN agent_configs config ON config.id = agent.current_config_id AND config.project_id = agent.project_id
JOIN projects project ON project.id = agent.project_id
JOIN orgs org ON org.id = project.org_id
WHERE agent.id = $1
  AND project.deleted_at IS NULL
  AND org.deleted_at IS NULL;


-- name: EnqueueEventWebhookDelivery :exec
INSERT INTO event_webhook_deliveries (org_id, agent_id, event_sequence, tool_call_id, tool_state)
VALUES ($1, $2, $3, $4, $5);

-- name: ClaimEventWebhookDelivery :one
WITH candidate AS (
    SELECT delivery.id
    FROM event_webhook_deliveries delivery
    WHERE delivery.next_attempt_at <= statement_timestamp()
      AND delivery.created_at > statement_timestamp() - interval '10 minutes'
      AND (delivery.claim_expires_at IS NULL OR delivery.claim_expires_at <= statement_timestamp())
      AND (coalesce(cardinality(sqlc.arg(excluded_org_ids)::uuid[]), 0) = 0
           OR NOT (delivery.org_id = ANY(sqlc.arg(excluded_org_ids)::uuid[])))
    ORDER BY delivery.next_attempt_at, delivery.id
    LIMIT 1
    FOR UPDATE OF delivery SKIP LOCKED
),
claimed AS (
    UPDATE event_webhook_deliveries delivery
    SET attempt_count = attempt_count + 1,
        claim_token = uuidv7(),
        claim_expires_at = statement_timestamp() + interval '30 seconds'
    FROM candidate
    WHERE delivery.id = candidate.id
    RETURNING delivery.id, delivery.agent_id, delivery.org_id, delivery.event_sequence, delivery.tool_call_id,
        delivery.tool_state, delivery.attempt_count, delivery.claim_token
)
SELECT claimed.id, claimed.agent_id, claimed.org_id, claimed.event_sequence, claimed.tool_call_id, claimed.tool_state,
       claimed.attempt_count, claimed.claim_token,
       coalesce(call.type, '')::text AS tool_type,
       coalesce(call.name, '')::text AS tool_name
FROM claimed
LEFT JOIN tool_calls call ON call.agent_id = claimed.agent_id AND call.id = claimed.tool_call_id;

-- name: CompleteEventWebhookDelivery :exec
DELETE FROM event_webhook_deliveries WHERE id = $1 AND claim_token = $2;

-- name: RetryEventWebhookDelivery :exec
UPDATE event_webhook_deliveries
SET next_attempt_at = statement_timestamp() + sqlc.arg(delay_seconds)::double precision * interval '1 second',
    claim_token = NULL, claim_expires_at = NULL
WHERE id = sqlc.arg(id) AND claim_token = sqlc.arg(claim_token);

-- name: DeleteExpiredEventWebhookDeliveries :execrows
WITH candidates AS (
    SELECT id FROM event_webhook_deliveries
    WHERE created_at <= statement_timestamp() - interval '10 minutes'
    ORDER BY created_at, id
    LIMIT sqlc.arg(limit_count)
    FOR UPDATE SKIP LOCKED
)
DELETE FROM event_webhook_deliveries delivery
USING candidates
WHERE delivery.id = candidates.id;
