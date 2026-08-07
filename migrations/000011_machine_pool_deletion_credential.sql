-- +goose Up

ALTER TABLE machine_pools
ADD COLUMN deletion_provider_auth_secret_version_id uuid;

UPDATE machine_pools AS pool
SET deletion_provider_auth_secret_version_id = coalesce(
    secret.current_version_id,
    (
        SELECT version.id
        FROM secret_versions AS version
        WHERE version.secret_id = pool.provider_auth_secret_id
        ORDER BY version.version_number DESC
        LIMIT 1
    )
)
FROM secrets AS secret
WHERE pool.management_kind = 'tenant'
  AND pool.deleted_at IS NOT NULL
  AND pool.provider_auth_secret_id IS NOT NULL
  AND secret.id = pool.provider_auth_secret_id;

ALTER TABLE machine_pools
ADD CONSTRAINT machine_pools_deletion_credential_version_fkey
    FOREIGN KEY (provider_auth_secret_id, deletion_provider_auth_secret_version_id)
    REFERENCES secret_versions(secret_id, id),
ADD CONSTRAINT machine_pools_deletion_credential_state_check CHECK (
    deletion_provider_auth_secret_version_id IS NULL OR
    (management_kind = 'tenant' AND provider_auth_secret_id IS NOT NULL AND deleted_at IS NOT NULL)
),
ADD CONSTRAINT machine_pools_deleted_tenant_credential_check CHECK (
    management_kind <> 'tenant' OR deleted_at IS NULL OR provider_auth_secret_id IS NULL OR
    deletion_provider_auth_secret_version_id IS NOT NULL
);
