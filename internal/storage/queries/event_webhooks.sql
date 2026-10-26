-- name: ListMatchingEventWebhooks :many
SELECT webhook.id, webhook.org_id, webhook.event_types
FROM agents agent
JOIN projects project ON project.id = agent.project_id
JOIN orgs org ON org.id = project.org_id
JOIN event_webhooks webhook ON webhook.org_id = agent.org_id AND webhook.project_id = agent.project_id AND (
    (webhook.agent_profile_id IS NULL AND webhook.agent_id IS NULL) OR
    webhook.agent_profile_id = agent.agent_profile_id OR
    (webhook.agent_profile_id IS NULL AND webhook.agent_id = agent.id)
)
WHERE agent.id = sqlc.arg(agent_id)
  AND webhook.deleted_at IS NULL
  AND project.deleted_at IS NULL
  AND org.deleted_at IS NULL
ORDER BY webhook.id;

-- name: GetEventWebhookEndpoint :one
SELECT webhook.org_id, webhook.project_id, webhook.url, webhook.signing_secret_id
FROM event_webhooks webhook
JOIN orgs org ON org.id = webhook.org_id AND org.deleted_at IS NULL
JOIN agents agent ON agent.project_id = webhook.project_id AND agent.id = sqlc.arg(agent_id)
JOIN projects project ON project.id = agent.project_id AND project.deleted_at IS NULL
WHERE webhook.org_id = sqlc.arg(org_id)
  AND webhook.id = sqlc.arg(webhook_id)
  AND webhook.deleted_at IS NULL;

-- name: InsertEventWebhook :one
INSERT INTO event_webhooks (org_id, project_id, agent_profile_id, agent_id, url, event_types, signing_secret_id)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, org_id, project_id, agent_profile_id, agent_id, url, event_types, signing_secret_id,
          created_at, updated_at, deleted_at;

-- name: GetEventWebhook :one
SELECT webhook.id, webhook.org_id, webhook.project_id, webhook.agent_profile_id, webhook.agent_id, webhook.url, webhook.event_types, webhook.signing_secret_id,
          webhook.created_at, webhook.updated_at, webhook.deleted_at FROM event_webhooks webhook
JOIN orgs org ON org.id = webhook.org_id AND org.deleted_at IS NULL
JOIN projects project ON project.id = webhook.project_id AND project.deleted_at IS NULL
WHERE webhook.project_id = $1 AND webhook.id = $2 AND webhook.deleted_at IS NULL;

-- name: GetEventWebhookForUpdate :one
SELECT id, org_id, project_id, agent_profile_id, agent_id, url, event_types, signing_secret_id,
       created_at, updated_at, deleted_at
FROM event_webhooks
WHERE project_id = $1 AND id = $2 AND deleted_at IS NULL
FOR UPDATE;

-- name: ListEventWebhooks :many
SELECT id, org_id, project_id, agent_profile_id, agent_id, url, event_types, signing_secret_id,
          created_at, updated_at, deleted_at FROM event_webhooks
WHERE project_id = sqlc.arg(project_id)
  AND agent_profile_id IS NOT DISTINCT FROM sqlc.narg(agent_profile_id)::uuid
  AND agent_id IS NOT DISTINCT FROM sqlc.narg(agent_id)::uuid
  AND deleted_at IS NULL
ORDER BY created_at, id;

-- name: UpdateEventWebhook :one
UPDATE event_webhooks SET url = $3, event_types = $4, signing_secret_id = $5, updated_at = statement_timestamp()
WHERE project_id = $1 AND id = $2 AND deleted_at IS NULL
RETURNING id, org_id, project_id, agent_profile_id, agent_id, url, event_types, signing_secret_id,
          created_at, updated_at, deleted_at;

-- name: DeleteEventWebhook :execrows
UPDATE event_webhooks SET deleted_at = statement_timestamp(), updated_at = statement_timestamp()
WHERE project_id = $1 AND id = $2 AND deleted_at IS NULL;

-- name: DeleteEventWebhooksForAgentProfile :exec
UPDATE event_webhooks SET deleted_at = statement_timestamp(), updated_at = statement_timestamp()
WHERE project_id = $1 AND agent_profile_id = $2 AND deleted_at IS NULL;


-- name: EnqueueEventWebhookDelivery :exec
INSERT INTO event_webhook_deliveries (org_id, webhook_id, agent_id, event_sequence, tool_call_id, tool_state, interaction_update)
VALUES ($1, $2, $3, $4, $5, $6, $7);

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
RETURNING delivery.id, delivery.webhook_id, delivery.agent_id, delivery.org_id, delivery.event_sequence, delivery.tool_call_id,
    delivery.tool_state, delivery.interaction_update, delivery.attempt_count, delivery.claim_token;

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
