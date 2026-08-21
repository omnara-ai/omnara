-- name: EnqueueDefaultModelProviderProvisioning :execrows
INSERT INTO default_model_provider_provisioning_jobs (
    organization_id,
    creator_user_id,
    provider_template
)
VALUES (
    sqlc.arg(organization_id),
    sqlc.arg(creator_user_id),
    sqlc.arg(provider_template)
)
ON CONFLICT (organization_id) DO NOTHING;

-- name: ClaimDefaultModelProviderProvisioning :one
WITH candidate AS (
    SELECT job.organization_id
    FROM default_model_provider_provisioning_jobs job
    JOIN orgs organization
      ON organization.id = job.organization_id
     AND organization.deleted_at IS NULL
    WHERE job.next_attempt_at <= statement_timestamp()
      AND (
          job.claim_expires_at IS NULL
          OR job.claim_expires_at <= statement_timestamp()
      )
    ORDER BY job.next_attempt_at, job.organization_id
    LIMIT 1
    FOR UPDATE OF job SKIP LOCKED
)
UPDATE default_model_provider_provisioning_jobs job
SET attempt_count = job.attempt_count + 1,
    claim_token = uuidv7(),
    claim_expires_at = statement_timestamp()
        + sqlc.arg(claim_lease_seconds)::bigint * interval '1 second',
    updated_at = statement_timestamp()
FROM candidate
WHERE job.organization_id = candidate.organization_id
RETURNING
    job.organization_id,
    job.creator_user_id,
    job.provider_template,
    job.attempt_count,
    job.claim_token;

-- name: LockDefaultModelProviderProvisioning :one
SELECT creator_user_id, provider_template
FROM default_model_provider_provisioning_jobs
WHERE organization_id = sqlc.arg(organization_id)
  AND claim_token = sqlc.arg(claim_token)::uuid
FOR UPDATE;

-- name: CompleteDefaultModelProviderProvisioning :execrows
DELETE FROM default_model_provider_provisioning_jobs
WHERE organization_id = sqlc.arg(organization_id)
  AND claim_token = sqlc.arg(claim_token)::uuid;

-- name: RetryDefaultModelProviderProvisioning :execrows
UPDATE default_model_provider_provisioning_jobs
SET next_attempt_at = statement_timestamp()
        + sqlc.arg(retry_delay_seconds)::bigint * interval '1 second',
    claim_token = NULL,
    claim_expires_at = NULL,
    updated_at = statement_timestamp()
WHERE organization_id = sqlc.arg(organization_id)
  AND claim_token = sqlc.arg(claim_token)::uuid;

-- name: DeleteDefaultModelProviderProvisioningForOrganization :exec
DELETE FROM default_model_provider_provisioning_jobs
WHERE organization_id = sqlc.arg(organization_id);
