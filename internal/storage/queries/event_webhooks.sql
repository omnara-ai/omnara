-- name: GetAgentEventWebhookTarget :one
SELECT agent.project_id, project.org_id,
       coalesce(config.compiled_definition->'event_webhook'->>'url', '')::text AS url,
       coalesce(config.compiled_definition->'event_webhook'->>'signing_secret_id', '')::text AS signing_secret_id,
       coalesce(config.compiled_definition->'event_webhook'->'events', '[]'::jsonb)::jsonb AS events
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
WITH saturated_orgs AS MATERIALIZED (
    SELECT org_id
    FROM event_webhook_deliveries
    WHERE claim_expires_at > statement_timestamp()
    GROUP BY org_id
    HAVING count(*) >= sqlc.arg(per_org_limit)::bigint
), candidate AS (
    SELECT delivery.id
    FROM event_webhook_deliveries delivery
    WHERE delivery.next_attempt_at <= statement_timestamp()
      AND delivery.created_at > statement_timestamp()
          - sqlc.arg(delivery_window_seconds)::double precision * interval '1 second'
      AND (delivery.claim_expires_at IS NULL OR delivery.claim_expires_at <= statement_timestamp())
      AND delivery.org_id NOT IN (SELECT org_id FROM saturated_orgs)
    ORDER BY delivery.next_attempt_at, delivery.id
    LIMIT 1
    FOR UPDATE OF delivery SKIP LOCKED
)
UPDATE event_webhook_deliveries delivery
SET attempt_count = attempt_count + 1,
    claim_token = uuidv7(),
    claim_expires_at = statement_timestamp() + interval '30 seconds'
FROM candidate
WHERE delivery.id = candidate.id
RETURNING delivery.id, delivery.agent_id, delivery.org_id, delivery.event_sequence, delivery.tool_call_id,
    delivery.tool_state, delivery.attempt_count, delivery.claim_token;

-- name: CompleteEventWebhookDelivery :exec
DELETE FROM event_webhook_deliveries WHERE id = $1 AND claim_token = $2;

-- name: RetryEventWebhookDelivery :one
UPDATE event_webhook_deliveries
SET next_attempt_at = statement_timestamp() + sqlc.arg(delay_seconds)::double precision * interval '1 second',
    claim_token = NULL, claim_expires_at = NULL
WHERE id = sqlc.arg(id) AND claim_token = sqlc.arg(claim_token)
RETURNING next_attempt_at, next_attempt_at >= created_at
    + sqlc.arg(delivery_window_seconds)::double precision * interval '1 second' AS gave_up;

-- name: DeleteExpiredEventWebhookDeliveries :execrows
WITH candidates AS (
    SELECT id FROM event_webhook_deliveries
    WHERE created_at <= statement_timestamp()
        - sqlc.arg(delivery_window_seconds)::double precision * interval '1 second'
      AND (claim_expires_at IS NULL OR claim_expires_at <= statement_timestamp())
    ORDER BY created_at, id
    LIMIT sqlc.arg(limit_count)
    FOR UPDATE SKIP LOCKED
)
DELETE FROM event_webhook_deliveries delivery
USING candidates
WHERE delivery.id = candidates.id;
