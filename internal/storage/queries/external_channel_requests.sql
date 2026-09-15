-- Accepted execution facts have one real owner. Mutation callers hold lifecycle
-- and agent locks; polling does not claim or schedule provider work.
-- name: InsertExternalChannelRequest :one
INSERT INTO external_channel_requests (
  project_id, agent_id, turn_id, integration_install_id, integration_target_id,
  integration_target_binding_id, tool_call_id, interaction_id, notice_key,
  operation, payload, deadline_at
)
SELECT agent.project_id, agent.id, turn.id, install.id, target.id, binding.id,
  sqlc.narg(tool_call_id)::uuid, sqlc.narg(interaction_id)::uuid, sqlc.narg(notice_key)::text,
  sqlc.arg(operation)::text,
  sqlc.arg(payload)::jsonb,
  statement_timestamp() + sqlc.arg(deadline_microseconds)::bigint * interval '1 microsecond'
FROM agents agent
JOIN projects project ON project.id = agent.project_id AND project.deleted_at IS NULL
JOIN orgs org ON org.id = project.org_id AND org.deleted_at IS NULL
JOIN agent_turns turn ON turn.agent_id = agent.id AND turn.id = sqlc.arg(turn_id)
JOIN integration_installs install
  ON install.project_id = agent.project_id AND install.id = sqlc.arg(integration_install_id)
 AND install.integration_kind = 'external' AND install.state = 'active' AND install.deleted_at IS NULL
JOIN integration_targets target
  ON target.project_id = install.project_id AND target.integration_install_id = install.id
 AND target.id = sqlc.arg(integration_target_id) AND target.deleted_at IS NULL
JOIN integration_target_bindings binding
  ON binding.project_id = target.project_id AND binding.integration_target_id = target.id
 AND binding.agent_id = agent.id AND binding.id = sqlc.arg(integration_target_binding_id)
 AND binding.revoked_at IS NULL
WHERE agent.project_id = sqlc.arg(project_id) AND agent.id = sqlc.arg(agent_id) AND agent.state = 'active'
  AND CASE sqlc.arg(operation)::text WHEN 'read' THEN binding.read_allowed ELSE binding.send_allowed END
  AND (sqlc.arg(payload)::jsonb->>'reply_channel_grants' IS NULL OR binding.reply_receive_allowed IS NOT NULL)
  AND (binding.integration_route_id IS NULL OR EXISTS (
    SELECT 1 FROM integration_routes route
    WHERE route.project_id = binding.project_id AND route.integration_install_id = binding.integration_install_id
      AND route.id = binding.integration_route_id AND route.state = 'active' AND route.deleted_at IS NULL
  ))
  AND (
    (sqlc.narg(tool_call_id)::uuid IS NOT NULL AND EXISTS (
      SELECT 1 FROM tool_call_read_projection call
      WHERE call.project_id = agent.project_id AND call.agent_id = agent.id AND call.turn_id = turn.id
        AND call.id = sqlc.narg(tool_call_id)::uuid AND call.type = 'built_in' AND call.state = 'ready'
        AND call.name = CASE sqlc.arg(operation)::text
          WHEN 'read' THEN 'read_channel' WHEN 'send' THEN 'send_channel_message' END
    ))
    OR (sqlc.narg(interaction_id)::uuid IS NOT NULL AND EXISTS (
      SELECT 1 FROM agent_interaction_read_projection interaction
      WHERE interaction.project_id = agent.project_id AND interaction.agent_id = agent.id AND interaction.turn_id = turn.id
        AND interaction.id = sqlc.narg(interaction_id)::uuid AND interaction.state = 'open'
        AND interaction.integration_target_id = target.id AND sqlc.arg(operation)::text = 'interaction'
    ))
    OR (sqlc.narg(notice_key)::text IS NOT NULL AND sqlc.arg(operation)::text = 'send')
  )
RETURNING id, project_id, agent_id, turn_id, integration_install_id,
  integration_target_id, integration_target_binding_id, tool_call_id, interaction_id,
  notice_key, operation, payload, deadline_at, created_at,
  state, result, state_reason_code, terminal_at;

-- name: GetExternalChannelRequest :one
SELECT id, project_id, agent_id, turn_id, integration_install_id,
  integration_target_id, integration_target_binding_id, tool_call_id, interaction_id,
  notice_key, operation, payload, deadline_at, created_at,
  state, result, state_reason_code, terminal_at
FROM external_channel_requests
WHERE project_id = sqlc.arg(project_id) AND integration_install_id = sqlc.arg(integration_install_id) AND id = sqlc.arg(id);

-- name: GetExternalChannelRequestForUpdate :one
SELECT id, project_id, agent_id, turn_id, integration_install_id,
  integration_target_id, integration_target_binding_id, tool_call_id, interaction_id,
  notice_key, operation, payload, deadline_at, created_at,
  state, result, state_reason_code, terminal_at
FROM external_channel_requests
WHERE project_id = sqlc.arg(project_id) AND integration_install_id = sqlc.arg(integration_install_id) AND id = sqlc.arg(id)
FOR UPDATE;

-- name: GetExternalChannelRequestByOwner :one
SELECT id, project_id, agent_id, turn_id, integration_install_id,
  integration_target_id, integration_target_binding_id, tool_call_id, interaction_id,
  notice_key, operation, payload, deadline_at, created_at,
  state, result, state_reason_code, terminal_at
FROM external_channel_requests
WHERE project_id = sqlc.arg(project_id) AND agent_id = sqlc.arg(agent_id) AND turn_id = sqlc.arg(turn_id)
  AND tool_call_id IS NOT DISTINCT FROM sqlc.narg(tool_call_id)::uuid
  AND interaction_id IS NOT DISTINCT FROM sqlc.narg(interaction_id)::uuid
  AND notice_key IS NOT DISTINCT FROM sqlc.narg(notice_key)::text;

-- name: ListPendingExternalChannelRequests :many
SELECT request.id, request.project_id, request.agent_id, request.turn_id, request.integration_install_id,
  request.integration_target_id, request.integration_target_binding_id, request.tool_call_id, request.interaction_id,
  request.notice_key, request.operation, request.payload, request.deadline_at, request.created_at,
  request.state, request.result, request.state_reason_code, request.terminal_at
FROM external_channel_requests request
JOIN integration_installs install
  ON install.project_id = request.project_id AND install.id = request.integration_install_id
 AND install.integration_kind = 'external' AND install.state = 'active' AND install.deleted_at IS NULL
JOIN projects project ON project.id = request.project_id AND project.deleted_at IS NULL
JOIN orgs org ON org.id = project.org_id AND org.deleted_at IS NULL
JOIN agents agent ON agent.project_id = request.project_id AND agent.id = request.agent_id AND agent.state = 'active'
JOIN integration_targets target
  ON target.project_id = request.project_id AND target.integration_install_id = request.integration_install_id
 AND target.id = request.integration_target_id AND target.deleted_at IS NULL
JOIN integration_channel_definitions definition
  ON definition.project_id = target.project_id AND definition.integration_install_id = target.integration_install_id
 AND definition.id = target.channel_definition_id
JOIN integration_target_bindings binding
  ON binding.project_id = request.project_id AND binding.agent_id = request.agent_id
 AND binding.integration_target_id = request.integration_target_id AND binding.id = request.integration_target_binding_id
 AND binding.revoked_at IS NULL
WHERE request.project_id = sqlc.arg(project_id) AND request.integration_install_id = sqlc.arg(integration_install_id)
  AND request.state = 'pending' AND request.deadline_at > statement_timestamp()
  AND (sqlc.narg(cursor_created_at)::timestamptz IS NULL
    OR (request.created_at, request.id) > (sqlc.narg(cursor_created_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
  AND CASE request.operation WHEN 'read' THEN binding.read_allowed ELSE binding.send_allowed END
  -- The accepted tuple is the delegation decision; later grants cannot enable it.
  AND (request.payload->>'reply_channel_grants' IS NULL OR (binding.reply_receive_allowed IS NOT NULL
    AND definition.capabilities->>'creates_reply_channel' = 'true'))
  AND (binding.integration_route_id IS NULL OR EXISTS (
    SELECT 1 FROM integration_routes route
    WHERE route.project_id = binding.project_id AND route.integration_install_id = binding.integration_install_id
      AND route.id = binding.integration_route_id AND route.state = 'active' AND route.deleted_at IS NULL
  ))
  AND (
    (request.tool_call_id IS NOT NULL AND definition.capabilities->>request.operation = 'true' AND EXISTS (
      SELECT 1 FROM tool_call_read_projection call
      WHERE call.project_id = request.project_id AND call.agent_id = request.agent_id AND call.turn_id = request.turn_id
        AND call.id = request.tool_call_id AND call.type = 'built_in' AND call.state = 'waiting'
        AND call.name = CASE request.operation WHEN 'read' THEN 'read_channel' WHEN 'send' THEN 'send_channel_message' END
    ))
    OR (request.interaction_id IS NOT NULL AND EXISTS (
      SELECT 1 FROM agent_interaction_read_projection interaction
      WHERE interaction.project_id = request.project_id AND interaction.agent_id = request.agent_id
        AND interaction.turn_id = request.turn_id AND interaction.id = request.interaction_id AND interaction.state = 'open'
        AND interaction.integration_target_id = request.integration_target_id
        AND definition.capabilities->>CASE interaction.interaction_kind
          WHEN 'permission' THEN 'permissions' WHEN 'question' THEN 'questions' END = 'true'
    ))
    OR (request.notice_key IS NOT NULL AND definition.capabilities->>'send' = 'true')
  )
ORDER BY request.created_at, request.id
LIMIT sqlc.arg(row_limit);

-- name: CompleteExternalChannelRequest :one
UPDATE external_channel_requests
SET state = 'completed', result = sqlc.arg(result)::jsonb, terminal_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND integration_install_id = sqlc.arg(integration_install_id) AND id = sqlc.arg(id)
  AND state = 'pending' AND deadline_at > statement_timestamp()
RETURNING id, project_id, agent_id, turn_id, integration_install_id,
  integration_target_id, integration_target_binding_id, tool_call_id, interaction_id,
  notice_key, operation, payload, deadline_at, created_at,
  state, result, state_reason_code, terminal_at;

-- name: TerminalizeExternalChannelRequest :one
UPDATE external_channel_requests
SET state = sqlc.arg(state)::text, state_reason_code = sqlc.arg(state_reason_code)::text,
  terminal_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND integration_install_id = sqlc.arg(integration_install_id) AND id = sqlc.arg(id)
  AND state = 'pending'
  AND (sqlc.arg(state)::text = 'canceled'
    OR (sqlc.arg(state)::text = 'expired' AND deadline_at <= statement_timestamp()))
RETURNING id, project_id, agent_id, turn_id, integration_install_id,
  integration_target_id, integration_target_binding_id, tool_call_id, interaction_id,
  notice_key, operation, payload, deadline_at, created_at,
  state, result, state_reason_code, terminal_at;

-- name: ListExpiredExternalChannelRequests :many
SELECT id, project_id, agent_id, turn_id, integration_install_id,
  integration_target_id, integration_target_binding_id, tool_call_id, interaction_id,
  notice_key, operation, payload, deadline_at, created_at,
  state, result, state_reason_code, terminal_at
FROM external_channel_requests
WHERE state = 'pending' AND deadline_at <= statement_timestamp()
ORDER BY deadline_at, id
LIMIT sqlc.arg(row_limit);

-- name: CancelExternalChannelRequestsForTurn :execrows
UPDATE external_channel_requests request
SET state = 'canceled', state_reason_code = sqlc.arg(reason)::text, terminal_at = statement_timestamp()
WHERE request.project_id = sqlc.arg(project_id) AND request.agent_id = sqlc.arg(agent_id)
  AND request.state = 'pending' AND request.turn_id = sqlc.arg(turn_id);

-- name: CancelExternalChannelRequestsForToolCall :execrows
UPDATE external_channel_requests request
SET state = 'canceled', state_reason_code = sqlc.arg(reason)::text, terminal_at = statement_timestamp()
WHERE request.project_id = sqlc.arg(project_id) AND request.agent_id = sqlc.arg(agent_id)
  AND request.state = 'pending' AND (request.tool_call_id = sqlc.arg(tool_call_id)::uuid OR EXISTS (
    SELECT 1 FROM agent_interactions interaction
    WHERE interaction.agent_id = request.agent_id AND interaction.id = request.interaction_id
      AND interaction.tool_call_id = sqlc.arg(tool_call_id)::uuid
  ));

-- name: CancelExternalChannelRequestsForInteraction :execrows
UPDATE external_channel_requests request
SET state = 'canceled', state_reason_code = sqlc.arg(reason)::text, terminal_at = statement_timestamp()
WHERE request.project_id = sqlc.arg(project_id) AND request.agent_id = sqlc.arg(agent_id)
  AND request.state = 'pending' AND request.interaction_id = sqlc.arg(interaction_id)::uuid;

-- name: GetExternalChannelRequestLifecycleScope :one
-- @sqlc-vet-disable integration-installs-deleted-at
-- Cleanup retains access to lifecycle gates after connection retirement.
SELECT org_id FROM integration_installs
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(integration_install_id)
  AND integration_kind = 'external';

-- name: CompleteToolCallFromExternalChannelRequest :one
WITH locked_agent AS MATERIALIZED (
  SELECT agent.project_id, agent.id FROM agents agent
  WHERE agent.project_id = sqlc.arg(project_id) AND agent.id = sqlc.arg(agent_id)
  FOR UPDATE
)
UPDATE tool_calls call
SET state = 'completed', runtime_lock_id = NULL
FROM external_channel_requests request
CROSS JOIN locked_agent agent
CROSS JOIN tool_call_read_projection projection
WHERE call.agent_id = agent.id AND call.id = request.tool_call_id AND call.state = 'waiting' AND call.type = 'built_in'
  AND request.project_id = agent.project_id AND request.agent_id = call.agent_id AND request.id = sqlc.arg(id)
  AND request.state IN ('completed', 'expired', 'canceled')
  AND call.name = CASE request.operation WHEN 'read' THEN 'read_channel' WHEN 'send' THEN 'send_channel_message' END
  AND projection.project_id = agent.project_id AND projection.agent_id = call.agent_id AND projection.id = call.id
  AND projection.turn_id = request.turn_id
RETURNING call.id, projection.project_id, call.agent_id,
  projection.turn_id, projection.source_event_id, projection.model_call_context_id,
  call.provider_call_id, call.name, call.input, call.type, call.state,
  sqlc.arg(outcome)::text AS outcome, call.runtime_lock_id,
  '[]'::jsonb AS result_content_parts, call.created_at;

-- The owning transaction has enumerated and locked every affected agent before
-- this update. Retirement cancels obligations even after routing/grants change.
-- name: CancelExternalChannelRequestsForInstallation :many
UPDATE external_channel_requests
SET state = 'canceled', state_reason_code = sqlc.arg(reason)::text, terminal_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND integration_install_id = sqlc.arg(integration_install_id)
  AND state = 'pending'
RETURNING id, project_id, agent_id, turn_id, integration_install_id,
  integration_target_id, integration_target_binding_id, tool_call_id, interaction_id,
  notice_key, operation, payload, deadline_at, created_at,
  state, result, state_reason_code, terminal_at;
