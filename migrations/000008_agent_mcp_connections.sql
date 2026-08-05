-- +goose Up

-- Per-agent MCP connection state.
-- agent_id is deliberately stored here: the parent
-- agent row owns the connection lifetime.
CREATE TABLE agent_mcp_connections (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    agent_id uuid NOT NULL,
    server_key text NOT NULL,
    endpoint_url text NOT NULL,
    config_hash text NOT NULL,
    state text NOT NULL,
    protocol_version text NOT NULL DEFAULT '',
    mcp_session_id text NOT NULL DEFAULT '',
    server_capabilities jsonb NOT NULL DEFAULT '{}'::jsonb,
    server_info jsonb NOT NULL DEFAULT '{}'::jsonb,
    tools_snapshot jsonb NOT NULL DEFAULT '[]'::jsonb,
    initialize_error text NOT NULL DEFAULT '',
    generation bigint NOT NULL DEFAULT 1,
    request_sequence bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK (server_key <> ''),
    CHECK (endpoint_url <> ''),
    CHECK (config_hash <> ''),
    CHECK (state IN ('initializing', 'ready', 'failed', 'expired')),
    CHECK (generation > 0),
    CHECK (request_sequence > 0),
    CHECK (jsonb_typeof(server_capabilities) = 'object'),
    CHECK (jsonb_typeof(server_info) = 'object'),
    CHECK (jsonb_typeof(tools_snapshot) = 'array'),
    FOREIGN KEY (agent_id) REFERENCES agents(id),
    UNIQUE (agent_id, server_key)
);
