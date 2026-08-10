-- name: GetOrCreateMCPConnection :one
INSERT INTO agent_mcp_connections(
  agent_id, server_key, endpoint_url,
  config_hash, state, created_at, updated_at
)
SELECT agent.id,
       sqlc.arg(server_key), sqlc.arg(endpoint_url),
       sqlc.arg(config_hash), 'initializing', statement_timestamp(), statement_timestamp()
FROM agents agent
WHERE agent.project_id = sqlc.arg(project_id)
  AND agent.id = sqlc.arg(agent_id)
ON CONFLICT (agent_id, server_key) DO UPDATE
SET endpoint_url = EXCLUDED.endpoint_url,
    config_hash = EXCLUDED.config_hash,
    state = CASE
      WHEN agent_mcp_connections.config_hash = EXCLUDED.config_hash THEN agent_mcp_connections.state
      ELSE 'initializing'
    END,
    protocol_version = CASE
      WHEN agent_mcp_connections.config_hash = EXCLUDED.config_hash THEN agent_mcp_connections.protocol_version
      ELSE ''
    END,
    mcp_session_id = CASE
      WHEN agent_mcp_connections.config_hash = EXCLUDED.config_hash THEN agent_mcp_connections.mcp_session_id
      ELSE ''
    END,
    server_capabilities = CASE
      WHEN agent_mcp_connections.config_hash = EXCLUDED.config_hash THEN agent_mcp_connections.server_capabilities
      ELSE '{}'::jsonb
    END,
    server_info = CASE
      WHEN agent_mcp_connections.config_hash = EXCLUDED.config_hash THEN agent_mcp_connections.server_info
      ELSE '{}'::jsonb
    END,
    tools_snapshot = CASE
      WHEN agent_mcp_connections.config_hash = EXCLUDED.config_hash THEN agent_mcp_connections.tools_snapshot
      ELSE '[]'::jsonb
    END,
    initialize_error = CASE
      WHEN agent_mcp_connections.config_hash = EXCLUDED.config_hash THEN agent_mcp_connections.initialize_error
      ELSE ''
    END,
    generation = CASE
      WHEN agent_mcp_connections.config_hash = EXCLUDED.config_hash THEN agent_mcp_connections.generation
      ELSE agent_mcp_connections.generation + 1
    END,
    updated_at = statement_timestamp()
RETURNING id, agent_id, server_key, endpoint_url, config_hash, state,
          protocol_version, mcp_session_id, server_capabilities, server_info,
          tools_snapshot, initialize_error, generation, request_sequence,
          created_at, updated_at;

-- name: GetMCPConnection :one
SELECT connection.id, connection.agent_id, connection.server_key,
       connection.endpoint_url, connection.config_hash, connection.state,
       connection.protocol_version, connection.mcp_session_id,
       connection.server_capabilities, connection.server_info,
       connection.tools_snapshot, connection.initialize_error,
       connection.generation, connection.request_sequence,
       connection.created_at, connection.updated_at
FROM agent_mcp_connections connection
JOIN agents agent ON agent.id = connection.agent_id
WHERE agent.project_id = sqlc.arg(project_id)
  AND connection.agent_id = sqlc.arg(agent_id)
  AND connection.server_key = sqlc.arg(server_key);

-- name: ListAgentMCPConnections :many
SELECT connection.id, connection.agent_id, connection.server_key,
       connection.endpoint_url, connection.config_hash, connection.state,
       connection.protocol_version, connection.mcp_session_id,
       connection.server_capabilities, connection.server_info,
       connection.tools_snapshot, connection.initialize_error,
       connection.generation, connection.request_sequence,
       connection.created_at, connection.updated_at
FROM agent_mcp_connections connection
JOIN agents agent ON agent.id = connection.agent_id
WHERE agent.project_id = sqlc.arg(project_id)
  AND connection.agent_id = sqlc.arg(agent_id)
ORDER BY connection.server_key;

-- name: GetMCPConnectionByID :one
SELECT connection.id, connection.agent_id, connection.server_key,
       connection.endpoint_url, connection.config_hash, connection.state,
       connection.protocol_version, connection.mcp_session_id,
       connection.server_capabilities, connection.server_info,
       connection.tools_snapshot, connection.initialize_error,
       connection.generation, connection.request_sequence,
       connection.created_at, connection.updated_at
FROM agent_mcp_connections connection
JOIN agents agent ON agent.id = connection.agent_id
WHERE agent.project_id = sqlc.arg(project_id)
  AND connection.agent_id = sqlc.arg(agent_id)
  AND connection.id = sqlc.arg(id);

-- name: MarkMCPConnectionReady :one
UPDATE agent_mcp_connections connection
SET state = 'ready',
    protocol_version = sqlc.arg(protocol_version),
    mcp_session_id = sqlc.arg(mcp_session_id),
    server_capabilities = sqlc.arg(server_capabilities),
    server_info = sqlc.arg(server_info),
    tools_snapshot = sqlc.arg(tools_snapshot),
    initialize_error = '',
    updated_at = transaction_timestamp()
FROM agents agent
WHERE agent.project_id = sqlc.arg(project_id)
  AND agent.id = connection.agent_id
  AND connection.agent_id = sqlc.arg(agent_id)
  AND connection.id = sqlc.arg(id)
  AND connection.state = 'initializing'
  AND connection.generation = sqlc.arg(generation_observed)
RETURNING connection.id, connection.agent_id, connection.server_key,
          connection.endpoint_url, connection.config_hash, connection.state,
          connection.protocol_version, connection.mcp_session_id,
          connection.server_capabilities, connection.server_info,
          connection.tools_snapshot, connection.initialize_error,
          connection.generation, connection.request_sequence,
          connection.created_at, connection.updated_at;

-- name: BeginMCPConnectionInitialization :one
UPDATE agent_mcp_connections connection
SET state = 'initializing',
    protocol_version = CASE WHEN connection.state IN ('failed', 'expired') THEN '' ELSE connection.protocol_version END,
    mcp_session_id = CASE WHEN connection.state IN ('failed', 'expired') THEN '' ELSE connection.mcp_session_id END,
    initialize_error = '',
    updated_at = transaction_timestamp()
FROM agents agent
WHERE agent.project_id = sqlc.arg(project_id)
  AND agent.id = connection.agent_id
  AND connection.agent_id = sqlc.arg(agent_id)
  AND connection.id = sqlc.arg(id)
  AND connection.state IN ('initializing', 'failed', 'expired')
RETURNING connection.id, connection.agent_id, connection.server_key,
          connection.endpoint_url, connection.config_hash, connection.state,
          connection.protocol_version, connection.mcp_session_id,
          connection.server_capabilities, connection.server_info,
          connection.tools_snapshot, connection.initialize_error,
          connection.generation, connection.request_sequence,
          connection.created_at, connection.updated_at;

-- name: MarkMCPConnectionFailed :one
UPDATE agent_mcp_connections connection
SET state = 'failed',
    protocol_version = '',
    mcp_session_id = '',
    server_capabilities = '{}'::jsonb,
    server_info = '{}'::jsonb,
    tools_snapshot = '[]'::jsonb,
    initialize_error = sqlc.arg(initialize_error),
    updated_at = transaction_timestamp()
FROM agents agent
WHERE agent.project_id = sqlc.arg(project_id)
  AND agent.id = connection.agent_id
  AND connection.agent_id = sqlc.arg(agent_id)
  AND connection.id = sqlc.arg(id)
  AND connection.state = 'initializing'
  AND connection.generation = sqlc.arg(generation_observed)
RETURNING connection.id, connection.agent_id, connection.server_key,
          connection.endpoint_url, connection.config_hash, connection.state,
          connection.protocol_version, connection.mcp_session_id,
          connection.server_capabilities, connection.server_info,
          connection.tools_snapshot, connection.initialize_error,
          connection.generation, connection.request_sequence,
          connection.created_at, connection.updated_at;

-- name: ExpireRemovedMCPConnections :exec
UPDATE agent_mcp_connections connection
SET state = 'expired',
    protocol_version = '',
    mcp_session_id = '',
    server_capabilities = '{}'::jsonb,
    server_info = '{}'::jsonb,
    tools_snapshot = '[]'::jsonb,
    generation = connection.generation + 1,
    updated_at = transaction_timestamp()
FROM agents agent
WHERE agent.project_id = sqlc.arg(project_id)
  AND agent.id = connection.agent_id
  AND connection.agent_id = sqlc.arg(agent_id)
  AND connection.server_key <> ALL(sqlc.arg(server_keys)::text[])
  AND connection.state <> 'expired';

-- name: MarkMCPConnectionExpired :one
UPDATE agent_mcp_connections connection
SET state = 'expired',
    protocol_version = '',
    mcp_session_id = '',
    server_capabilities = '{}'::jsonb,
    server_info = '{}'::jsonb,
    tools_snapshot = '[]'::jsonb,
    generation = generation + 1,
    updated_at = transaction_timestamp()
FROM agents agent
WHERE agent.project_id = sqlc.arg(project_id)
  AND agent.id = connection.agent_id
  AND connection.agent_id = sqlc.arg(agent_id)
  AND connection.id = sqlc.arg(id)
  AND connection.generation = sqlc.arg(generation_observed)
RETURNING connection.id, connection.agent_id, connection.server_key,
          connection.endpoint_url, connection.config_hash, connection.state,
          connection.protocol_version, connection.mcp_session_id,
          connection.server_capabilities, connection.server_info,
          connection.tools_snapshot, connection.initialize_error,
          connection.generation, connection.request_sequence,
          connection.created_at, connection.updated_at;

-- name: NextMCPRequestSequence :one
UPDATE agent_mcp_connections connection
SET request_sequence = request_sequence + 1
FROM agents agent
WHERE agent.project_id = sqlc.arg(project_id)
  AND agent.id = connection.agent_id
  AND connection.agent_id = sqlc.arg(agent_id)
  AND connection.id = sqlc.arg(id)
RETURNING (connection.request_sequence - 1)::bigint AS request_sequence;
