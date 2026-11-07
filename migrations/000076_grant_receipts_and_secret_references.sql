-- +goose Up

-- This transaction changes several grant, provider, webhook, and secret relations.
-- Live traffic can abort it with 40P01 (deadlock) or 55P03 (lock timeout); it is
-- safe to rerun. Quiesce relevant application mutations for a predictable rollout.

-- Grants remain hard-deleted authorization edges. Creation receipts retain consumed
-- keys independently, including after revocation and lifecycle cleanup.

ALTER TABLE project_machine_grants ADD UNIQUE (project_id, id);

CREATE TABLE project_machine_grant_creations (
    org_id uuid NOT NULL,
    project_id uuid NOT NULL,
    idempotency_key text NOT NULL CHECK (idempotency_key <> ''),
    request_digest text NOT NULL CHECK (request_digest ~ '^[0-9a-f]{64}$'),
    grant_id uuid,
    created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    PRIMARY KEY (project_id, idempotency_key),
    UNIQUE (project_id, grant_id),
    FOREIGN KEY (org_id, project_id) REFERENCES projects(org_id, id),
    FOREIGN KEY (project_id, grant_id) REFERENCES project_machine_grants(project_id, id)
        ON DELETE SET NULL (grant_id)
);

-- Original create bodies were not retained before this migration. Backfill the
-- current normalized grant, matching the old replay comparison at this boundary.
INSERT INTO project_machine_grant_creations(org_id, project_id, idempotency_key, request_digest, grant_id, created_at)
SELECT org_id, project_id, idempotency_key,
       encode(sha256(convert_to(jsonb_build_object(
           'machine_id', machine_id,
           'description', description,
           'metadata', metadata
       )::text, 'UTF8')), 'hex'), id, created_at
FROM project_machine_grants
WHERE idempotency_key IS NOT NULL AND source_kind = 'explicit';

ALTER TABLE project_machine_grants DROP COLUMN idempotency_key;

CREATE TABLE project_machine_pool_grant_creations (
    org_id uuid NOT NULL,
    project_id uuid NOT NULL,
    idempotency_key text NOT NULL CHECK (idempotency_key <> ''),
    request_digest text NOT NULL CHECK (request_digest ~ '^[0-9a-f]{64}$'),
    grant_id uuid,
    created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    PRIMARY KEY (project_id, idempotency_key),
    UNIQUE (project_id, grant_id),
    FOREIGN KEY (org_id, project_id) REFERENCES projects(org_id, id),
    FOREIGN KEY (project_id, grant_id) REFERENCES project_machine_pool_grants(project_id, id)
        ON DELETE SET NULL (grant_id)
);

-- Original create bodies were not retained before this migration. Backfill the
-- current normalized grant, matching the old replay comparison at this boundary.
INSERT INTO project_machine_pool_grant_creations(org_id, project_id, idempotency_key, request_digest, grant_id, created_at)
SELECT org_id, project_id, idempotency_key,
       encode(sha256(convert_to(jsonb_build_object(
           'machine_pool_id', machine_pool_id,
           'description', description,
           'default_machine_cpu', default_machine_cpu,
           'default_machine_memory_mb', default_machine_memory_mb,
           'default_machine_env_overlay', default_machine_env_overlay,
           'default_machine_secret_env_overlay', default_machine_secret_env_overlay,
           'default_machine_provider_options_overlay', default_machine_provider_options_overlay,
           'default_cwd', default_cwd,
           'max_total_machines', max_total_machines,
           'max_total_cpu', max_total_cpu,
           'max_total_memory_mb', max_total_memory_mb,
           'min_machine_cpu', min_machine_cpu,
           'min_machine_memory_mb', min_machine_memory_mb,
           'max_machine_cpu', max_machine_cpu,
           'max_machine_memory_mb', max_machine_memory_mb,
           'delete_after_idle_minutes', delete_after_idle_minutes,
           'metadata', metadata
       )::text, 'UTF8')), 'hex'), id, created_at
FROM project_machine_pool_grants
WHERE idempotency_key IS NOT NULL;

ALTER TABLE project_machine_pool_grants DROP COLUMN idempotency_key;


-- Durable provider header references use the same authority boundary as credentials.
ALTER TABLE model_provider_configs ADD UNIQUE (org_id, id, management_kind);
CREATE TABLE model_provider_header_secrets (
    org_id uuid NOT NULL,
    provider_config_id uuid NOT NULL,
    header_name text NOT NULL CHECK (header_name <> ''),
    secret_id uuid NOT NULL,
    management_kind text NOT NULL,
    PRIMARY KEY (org_id, provider_config_id, header_name),
    FOREIGN KEY (org_id, provider_config_id, management_kind)
        REFERENCES model_provider_configs(org_id, id, management_kind),
    FOREIGN KEY (org_id, secret_id, management_kind)
        REFERENCES secrets(org_id, id, management_kind)
);
CREATE INDEX model_provider_header_secrets_secret_idx
    ON model_provider_header_secrets(org_id, secret_id);
-- Preserve configured references even when their secret was already soft-deleted:
-- resolution must fail closed until the provider configuration is repaired.
INSERT INTO model_provider_header_secrets(org_id, provider_config_id, header_name, secret_id, management_kind)
SELECT config.org_id, config.id, header.key, header.value::uuid, config.management_kind
FROM model_provider_configs config
CROSS JOIN LATERAL jsonb_each_text(config.secret_headers) header
WHERE config.deleted_at IS NULL;
ALTER TABLE model_provider_configs DROP COLUMN secret_headers;

-- Every live consumer reference has an indexed reverse lookup for deletion guards.
CREATE INDEX model_provider_configs_credential_secret_idx
    ON model_provider_configs(org_id, credential_secret_id) WHERE credential_secret_id IS NOT NULL;
CREATE INDEX machine_pools_provider_auth_secret_idx
    ON machine_pools(org_id, provider_auth_secret_id) WHERE provider_auth_secret_id IS NOT NULL;
CREATE INDEX event_webhooks_signing_secret_idx
    ON event_webhooks(org_id, signing_secret_id) WHERE signing_secret_id IS NOT NULL;

-- Deleted scopes cannot receive deliveries or retain ownership of signing secrets.
UPDATE event_webhooks webhook
SET deleted_at = coalesce(webhook.deleted_at, statement_timestamp()),
    signing_secret_id = NULL, updated_at = statement_timestamp()
WHERE webhook.deleted_at IS NOT NULL OR EXISTS (
    SELECT 1 FROM projects project JOIN orgs org ON org.id = project.org_id
    WHERE project.id = webhook.project_id AND (project.deleted_at IS NOT NULL OR org.deleted_at IS NOT NULL)
);
ALTER TABLE event_webhooks ADD CONSTRAINT event_webhooks_deleted_signing_secret_check
    CHECK (deleted_at IS NULL OR signing_secret_id IS NULL);
