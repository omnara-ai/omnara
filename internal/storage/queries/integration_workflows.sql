-- A project-local advisory lock serializes the first event of one behavior
-- instance, following the existing launch-idempotency locking pattern. It does
-- not hold across media downloads or provider I/O.
-- name: LockIntegrationWorkflowIdentity :exec
SELECT pg_advisory_xact_lock(hashtextextended(
  sqlc.arg(project_id)::uuid::text || ':' || sqlc.arg(integration_install_id)::uuid::text || ':' ||
  sqlc.arg(integration_route_id)::uuid::text || ':' || sqlc.arg(instance_key)::text, 0
));

-- name: GetIntegrationWorkflowAgent :one
SELECT agent_id
FROM integration_workflows
WHERE project_id = sqlc.arg(project_id)
  AND integration_install_id = sqlc.arg(integration_install_id)
  AND integration_route_id = sqlc.arg(integration_route_id)
  AND instance_key = sqlc.arg(instance_key);

-- name: InsertIntegrationWorkflow :exec
INSERT INTO integration_workflows (
  project_id, integration_install_id, integration_route_id, instance_key, agent_id, created_at
) VALUES (
  sqlc.arg(project_id), sqlc.arg(integration_install_id), sqlc.arg(integration_route_id),
  sqlc.arg(instance_key), sqlc.arg(agent_id), statement_timestamp()
);

-- name: LockIntegrationWorkflowRoute :one
SELECT route.id, route.agent_profile_id
FROM integration_routes route
WHERE route.project_id = sqlc.arg(project_id)
  AND route.integration_install_id = sqlc.arg(integration_install_id)
  AND route.id = sqlc.arg(integration_route_id)
  AND route.state = 'active' AND route.deleted_at IS NULL
FOR SHARE;

-- A callback cannot use an expired or superseded receipt to create new agent
-- state. Parent and recipient locks are acquired before this fence.
-- name: LockIntegrationEventForExecution :one
SELECT receipt.event_id
FROM integration_event_receipts receipt
WHERE receipt.project_id = sqlc.arg(project_id)
  AND receipt.integration_install_id = sqlc.arg(integration_install_id)
  AND receipt.id = sqlc.arg(id)
  AND receipt.state = 'processing'
  AND receipt.lease_token = sqlc.arg(lease_token)
  AND receipt.lease_generation = sqlc.arg(lease_generation)
  AND receipt.lease_expires_at > statement_timestamp()
FOR SHARE;
