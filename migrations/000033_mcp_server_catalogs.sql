-- +goose Up

CREATE TABLE mcp_server_catalogs (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id uuid NOT NULL REFERENCES orgs(id),
    endpoint_url text NOT NULL,
    secret_id uuid,
    secret_version_id uuid,
    aws_region text NOT NULL DEFAULT '',
    aws_service text NOT NULL DEFAULT '',
    revision bigint NOT NULL DEFAULT 0,
    protocol_version text NOT NULL DEFAULT '',
    server_capabilities jsonb NOT NULL DEFAULT '{}'::jsonb,
    server_info jsonb NOT NULL DEFAULT '{}'::jsonb,
    instructions text NOT NULL DEFAULT '',
    discover_cache_scope text NOT NULL DEFAULT '',
    discover_ttl_ms integer NOT NULL DEFAULT 0,
    discover_expires_at timestamptz,
    tools_snapshot jsonb NOT NULL DEFAULT '[]'::jsonb,
    tools_cache_scope text NOT NULL DEFAULT '',
    tools_ttl_ms integer NOT NULL DEFAULT 0,
    tools_expires_at timestamptz,
    fetched_at timestamptz,
    refresh_owner_token uuid,
    refresh_lease_expires_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT mcp_server_catalogs_identity_key
        UNIQUE NULLS NOT DISTINCT (org_id, endpoint_url, secret_id, secret_version_id, aws_region, aws_service),
    CHECK (endpoint_url <> ''),
    CHECK ((secret_id IS NULL) = (secret_version_id IS NULL)),
    CHECK (revision >= 0),
    CHECK ((fetched_at IS NULL) = (revision = 0)),
    CHECK (fetched_at IS NULL OR protocol_version <> ''),
    CHECK (discover_ttl_ms >= 0),
    CHECK (tools_ttl_ms >= 0),
    CHECK (discover_cache_scope IN ('', 'public', 'private')),
    CHECK (tools_cache_scope IN ('', 'public', 'private')),
    CHECK (jsonb_typeof(server_capabilities) = 'object'),
    CHECK (jsonb_typeof(server_info) = 'object'),
    CHECK (jsonb_typeof(tools_snapshot) = 'array'),
    CHECK ((refresh_owner_token IS NULL) = (refresh_lease_expires_at IS NULL)),
    FOREIGN KEY (org_id, secret_id) REFERENCES secrets(org_id, id) ON DELETE CASCADE,
    FOREIGN KEY (secret_id, secret_version_id) REFERENCES secret_versions(secret_id, id) ON DELETE CASCADE
);

CREATE INDEX mcp_server_catalogs_secret_version_idx
    ON mcp_server_catalogs(secret_id, secret_version_id);

ALTER TABLE agent_mcp_connections
    ADD COLUMN catalog_id uuid REFERENCES mcp_server_catalogs(id) ON DELETE SET NULL;

ALTER TABLE agent_mcp_connections
    ADD CONSTRAINT agent_mcp_connections_stateless_has_no_session
    CHECK (protocol_version < '2026-07-28' OR mcp_session_id = '');

CREATE INDEX agent_mcp_connections_catalog_idx
    ON agent_mcp_connections(catalog_id);
