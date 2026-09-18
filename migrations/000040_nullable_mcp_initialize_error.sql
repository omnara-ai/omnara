-- +goose Up

ALTER TABLE agent_mcp_connections
    ALTER COLUMN initialize_error DROP NOT NULL,
    ALTER COLUMN initialize_error DROP DEFAULT;

UPDATE agent_mcp_connections
SET initialize_error = NULL
WHERE initialize_error IS NOT NULL
  AND (initialize_error = '' OR state IN ('ready', 'initializing'));

ALTER TABLE agent_mcp_connections
    ADD CONSTRAINT agent_mcp_connections_initialize_error_state_check
    CHECK (initialize_error IS NULL OR (state IN ('failed', 'expired') AND initialize_error <> ''));
