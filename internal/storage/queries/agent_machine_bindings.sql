-- name: InsertAgentMachineBinding :one
INSERT INTO agent_machine_bindings(org_id, project_id, agent_id, create_tool_call_id, machine_id, machine_ref, binding_kind, state, description, cwd, env_overlay, secret_env_overlay, delete_after_idle_minutes, metadata, created_at, updated_at)
SELECT agent.org_id, agent.project_id, agent.id, sqlc.narg(create_tool_call_id)::uuid, pmgrant.machine_id, sqlc.arg(machine_ref), sqlc.arg(binding_kind), 'attached', sqlc.arg(description), sqlc.arg(cwd), sqlc.arg(env_overlay)::jsonb, sqlc.arg(secret_env_overlay)::jsonb, sqlc.narg(delete_after_idle_minutes)::integer, sqlc.arg(metadata), statement_timestamp(), statement_timestamp()
FROM agents agent
JOIN project_machine_grants pmgrant ON pmgrant.project_id = agent.project_id
  AND pmgrant.id = sqlc.arg(project_machine_grant_id)
JOIN machines machine ON machine.org_id = agent.org_id
  AND machine.id = pmgrant.machine_id
  AND machine.deleted_at IS NULL
  AND (
    machine.lifecycle_state = 'active'
    OR (pmgrant.source_kind = 'pool' AND machine.source_kind = 'pool')
  )
WHERE agent.project_id = sqlc.arg(project_id) AND agent.id = sqlc.arg(agent_id)
  AND (
    sqlc.arg(binding_kind)::text = 'explicit'
    OR (
      sqlc.arg(binding_kind)::text = 'pool'
      AND pmgrant.source_kind = 'pool'
      AND machine.source_kind = 'pool'
    )
  )
ON CONFLICT (project_id, agent_id, machine_id) WHERE state = 'attached' DO NOTHING
RETURNING id, org_id, project_id, agent_id, create_tool_call_id, delete_tool_call_id, machine_id, machine_ref, binding_kind, state, description, cwd, env_overlay, secret_env_overlay, metadata, created_at, updated_at, delete_after_idle_minutes;

-- name: GetAgentMachineBindingByCreateToolCall :one
SELECT id, org_id, project_id, agent_id, create_tool_call_id, delete_tool_call_id, machine_id, machine_ref, binding_kind, state, description, cwd, env_overlay, secret_env_overlay, metadata, created_at, updated_at, delete_after_idle_minutes
FROM agent_machine_bindings
WHERE project_id = sqlc.arg(project_id)
  AND agent_id = sqlc.arg(agent_id)
  AND create_tool_call_id = sqlc.arg(create_tool_call_id)::uuid;

-- name: GetAgentMachineBindingByDeleteToolCall :one
SELECT id, org_id, project_id, agent_id, create_tool_call_id, delete_tool_call_id, machine_id, machine_ref, binding_kind, state, description, cwd, env_overlay, secret_env_overlay, metadata, created_at, updated_at, delete_after_idle_minutes
FROM agent_machine_bindings
WHERE project_id = sqlc.arg(project_id)
  AND agent_id = sqlc.arg(agent_id)
  AND delete_tool_call_id = sqlc.arg(delete_tool_call_id)::uuid;

-- name: GetToolCallAgentConfigID :one
SELECT context.agent_config_id
FROM tool_call_read_projection tool_call
JOIN model_call_contexts context ON context.project_id = tool_call.project_id
  AND context.agent_id = tool_call.agent_id
  AND context.id = tool_call.model_call_context_id
WHERE tool_call.project_id = sqlc.arg(project_id)
  AND tool_call.agent_id = sqlc.arg(agent_id)
  AND tool_call.id = sqlc.arg(tool_call_id);

-- name: GetAgentMachineBindingByMachine :one
SELECT id, org_id, project_id, agent_id, create_tool_call_id, delete_tool_call_id, machine_id, machine_ref, binding_kind, state, description, cwd, env_overlay, secret_env_overlay, metadata, created_at, updated_at, delete_after_idle_minutes
FROM agent_machine_bindings
WHERE project_id = sqlc.arg(project_id)
  AND agent_id = sqlc.arg(agent_id)
  AND machine_id = sqlc.arg(machine_id)
  AND binding_kind = sqlc.arg(binding_kind)
  AND (sqlc.arg(include_released)::boolean OR state = 'attached')
ORDER BY created_at, id
LIMIT 1;

-- name: CountActiveAgentPoolMachines :one
-- @sqlc-vet-disable machines-deleted-at
-- lifecycle_state <> 'deleted' also excludes soft-deleted machines: DeleteMachine sets both together.
SELECT count(*)::integer
FROM agent_machine_bindings binding
JOIN machines machine ON machine.org_id = binding.org_id
  AND machine.id = binding.machine_id
WHERE binding.project_id = sqlc.arg(project_id)
  AND binding.agent_id = sqlc.arg(agent_id)
  AND binding.binding_kind = 'pool'
  AND binding.state = 'attached'
  AND machine.source_kind = 'pool'
  AND machine.machine_pool_id = sqlc.arg(machine_pool_id)::uuid
  AND machine.lifecycle_state <> 'deleted';

-- name: SelectPoolMachines :many
SELECT binding.id,
       binding.org_id,
       binding.project_id,
       binding.agent_id,
       binding.create_tool_call_id,
       binding.delete_tool_call_id,
       binding.machine_id,
       binding.machine_ref,
       binding.binding_kind,
       binding.state,
       binding.description,
       binding.cwd,
       binding.env_overlay,
       binding.secret_env_overlay,
       binding.delete_after_idle_minutes,
       binding.metadata,
       binding.created_at,
       binding.updated_at,
       machine.machine_pool_id,
       machine.source_kind AS machine_source_kind,
       machine.display_name AS machine_display_name,
       machine.description AS machine_description,
       machine.provider,
       machine.lifecycle_state,
       machine.failure_report,
       machine.provider_resource_id,
       machine.provider_provision_attempted_at,
       connection.connection_state,
       coalesce(current_runtime.state_reason_code, '') AS connection_state_reason,
       machine.last_observed_at,
       machine.cpu,
       machine.memory_mb,
       machine.cwd AS machine_cwd,
       machine.env AS machine_env,
       machine.secret_env AS machine_secret_env,
       machine.provider_options,
       coalesce(machine.idempotency_key, '') AS machine_idempotency_key,
       coalesce(machine.lifecycle_reason_code, '') AS lifecycle_reason_code,
       machine.lifecycle_reason_message,
       machine.next_reconcile_after,
       machine.provision_attempts,
       machine.delete_attempts,
       machine.metadata AS machine_metadata,
       machine.deleted_at,
       machine.created_at AS machine_created_at,
       machine.updated_at AS machine_updated_at,
       machine.lifecycle_changed_at,
       machine.lifecycle_version,
       pool.name AS pool_name
FROM agent_machine_bindings binding
JOIN machines machine ON machine.org_id = binding.org_id
  AND machine.id = binding.machine_id
  AND machine.source_kind = 'pool'
JOIN machine_pools pool ON pool.org_id = machine.org_id
  AND pool.id = machine.machine_pool_id
JOIN machine_connection_states connection ON connection.org_id = machine.org_id
  AND connection.machine_id = machine.id
LEFT JOIN daemon_runtimes current_runtime ON current_runtime.org_id = machine.org_id
  AND current_runtime.id = machine.current_daemon_runtime_id
WHERE binding.project_id = sqlc.arg(project_id)
  AND binding.agent_id = sqlc.arg(agent_id)
  AND binding.binding_kind = sqlc.arg(binding_kind)
  AND (sqlc.narg(machine_ref)::text IS NULL OR binding.machine_ref = sqlc.narg(machine_ref)::text)
  AND (sqlc.arg(include_released)::boolean OR binding.state = 'attached')
ORDER BY binding.created_at, binding.id;

-- name: MarkAgentMachineBindingDeleteRequested :one
UPDATE agent_machine_bindings
SET delete_tool_call_id = sqlc.arg(delete_tool_call_id)::uuid,
    updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id)
  AND agent_id = sqlc.arg(agent_id)
  AND id = sqlc.arg(id)
  AND state = 'attached'
  AND delete_tool_call_id IS NULL
RETURNING id, org_id, project_id, agent_id, create_tool_call_id, delete_tool_call_id, machine_id, machine_ref, binding_kind, state, description, cwd, env_overlay, secret_env_overlay, metadata, created_at, updated_at, delete_after_idle_minutes;

-- name: UpdateAttachedAgentMachineBindingConfig :execrows
UPDATE agent_machine_bindings
SET description = sqlc.arg(description),
    cwd = sqlc.arg(cwd),
    env_overlay = sqlc.arg(env_overlay)::jsonb,
    secret_env_overlay = sqlc.arg(secret_env_overlay)::jsonb,
    delete_after_idle_minutes = sqlc.narg(delete_after_idle_minutes)::integer,
    updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id)
  AND agent_id = sqlc.arg(agent_id)
  AND id = sqlc.arg(id)
  AND state = 'attached';

-- name: ReleaseExplicitAgentMachineBinding :execrows
UPDATE agent_machine_bindings
SET state = 'released',
    updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id)
  AND agent_id = sqlc.arg(agent_id)
  AND machine_id = sqlc.arg(machine_id)
  AND binding_kind = 'explicit'
  AND state = 'attached';

-- name: LockAgentMachineSources :exec
SELECT pg_advisory_xact_lock(
  hashtextextended('agent_machine_sources:' || sqlc.arg(agent_id)::uuid::text, 0)
);

-- name: LockAttachedAgentPoolMachines :exec
SELECT machine.id
FROM machines machine
JOIN agent_machine_bindings binding ON binding.org_id = machine.org_id
  AND binding.machine_id = machine.id
WHERE binding.project_id = sqlc.arg(project_id)
  AND binding.agent_id = sqlc.arg(agent_id)
  AND binding.binding_kind = 'pool'
  AND binding.state = 'attached'
  AND machine.source_kind = 'pool'
  AND machine.deleted_at IS NULL
  AND machine.lifecycle_state NOT IN ('deleting', 'delete_failed', 'deleted')
ORDER BY machine.id
FOR UPDATE OF machine;

-- name: MarkRemovedAgentPoolSourceMachinesDeleting :many
UPDATE machines machine
SET lifecycle_state = 'deleting',
    lifecycle_changed_at = statement_timestamp(),
    lifecycle_version = machine.lifecycle_version + 1,
    lifecycle_reason_code = sqlc.narg(lifecycle_reason_code),
    lifecycle_reason_message = sqlc.arg(lifecycle_reason_message),
    next_reconcile_after = statement_timestamp(),
    provider_runtime_mismatch_since = NULL,
    wake_attempt_expires_at = NULL,
    updated_at = statement_timestamp()
FROM agent_machine_bindings binding
WHERE binding.project_id = sqlc.arg(project_id)
  AND binding.agent_id = sqlc.arg(agent_id)
  AND binding.binding_kind = 'pool'
  AND binding.state = 'attached'
  AND machine.org_id = binding.org_id
  AND machine.id = binding.machine_id
  AND machine.source_kind = 'pool'
  AND machine.machine_pool_id = sqlc.arg(machine_pool_id)::uuid
  AND machine.deleted_at IS NULL
  AND machine.lifecycle_state NOT IN ('deleting', 'delete_failed', 'deleted')
RETURNING machine.id, machine.org_id, machine.machine_pool_id, machine.source_kind, machine.display_name, machine.description, machine.provider, machine.lifecycle_state, machine.provider_resource_id, machine.provider_provision_attempted_at, 'offline'::text AS connection_state, machine.last_observed_at, machine.cpu, machine.memory_mb, machine.cwd, machine.env, machine.secret_env, machine.provider_options, coalesce(machine.idempotency_key, '') AS idempotency_key, coalesce(machine.lifecycle_reason_code, '') AS lifecycle_reason_code, machine.lifecycle_reason_message, machine.next_reconcile_after, machine.provision_attempts, machine.delete_attempts, machine.metadata, machine.deleted_at, machine.created_at, machine.updated_at, machine.lifecycle_changed_at, machine.lifecycle_version;

-- name: MarkArchivedAgentPoolMachinesDeleting :many
UPDATE machines machine
SET lifecycle_state = 'deleting',
    lifecycle_changed_at = agent.archived_at,
    lifecycle_version = machine.lifecycle_version + 1,
    lifecycle_reason_code = sqlc.narg(lifecycle_reason_code),
    lifecycle_reason_message = sqlc.arg(lifecycle_reason_message),
    next_reconcile_after = agent.archived_at,
    provider_runtime_mismatch_since = NULL,
    wake_attempt_expires_at = NULL,
    updated_at = agent.archived_at
FROM agent_machine_bindings binding
JOIN agents agent ON agent.org_id = binding.org_id
  AND agent.project_id = binding.project_id
  AND agent.id = binding.agent_id
WHERE binding.project_id = sqlc.arg(project_id)
  AND binding.agent_id = sqlc.arg(agent_id)
  AND binding.binding_kind = 'pool'
  AND binding.state = 'attached'
  AND machine.org_id = binding.org_id
  AND machine.id = binding.machine_id
  AND machine.source_kind = 'pool'
  AND agent.state = 'archived'
  AND agent.archived_at IS NOT NULL
  AND machine.deleted_at IS NULL
  AND machine.lifecycle_state NOT IN ('deleting', 'delete_failed', 'deleted')
RETURNING machine.id, machine.org_id, machine.machine_pool_id, machine.source_kind, machine.display_name, machine.description, machine.provider, machine.lifecycle_state, machine.provider_resource_id, machine.provider_provision_attempted_at, 'offline'::text AS connection_state, machine.last_observed_at, machine.cpu, machine.memory_mb, machine.cwd, machine.env, machine.secret_env, machine.provider_options, coalesce(machine.idempotency_key, '') AS idempotency_key, coalesce(machine.lifecycle_reason_code, '') AS lifecycle_reason_code, machine.lifecycle_reason_message, machine.next_reconcile_after, machine.provision_attempts, machine.delete_attempts, machine.metadata, machine.deleted_at, machine.created_at, machine.updated_at, machine.lifecycle_changed_at, machine.lifecycle_version;

-- name: ListExecutableAgentMachineBindings :many
SELECT binding.id, binding.org_id, binding.project_id, binding.agent_id,
       binding.create_tool_call_id, binding.delete_tool_call_id, binding.machine_id,
       binding.machine_ref, binding.binding_kind, binding.state, binding.description,
       binding.env_overlay, binding.secret_env_overlay, binding.metadata,
       binding.created_at, binding.updated_at,
       coalesce(nullif(binding.cwd, ''), machine.cwd, '') AS effective_cwd
FROM agent_machine_bindings binding
JOIN project_machine_grants pmgrant ON pmgrant.project_id = binding.project_id
  AND pmgrant.machine_id = binding.machine_id
JOIN machines machine ON machine.org_id = binding.org_id
  AND machine.id = binding.machine_id
  AND machine.deleted_at IS NULL
  AND machine.lifecycle_state = 'active'
JOIN machine_connection_states connection ON connection.org_id = machine.org_id
  AND connection.machine_id = machine.id
WHERE binding.project_id = sqlc.arg(project_id)
  AND binding.agent_id = sqlc.arg(agent_id)
  AND binding.state = 'attached'
  AND connection.connection_state IN ('online', 'asleep')
ORDER BY binding.created_at, binding.id;

-- name: SelectAgentMachineObservations :many
SELECT binding.machine_ref,
       binding.binding_kind,
       binding.state AS binding_state,
       binding.description,
       coalesce(nullif(binding.cwd, ''), machine.cwd, '') AS effective_cwd,
       binding.created_at,
       binding.updated_at,
       machine.source_kind,
       machine.display_name,
       machine.lifecycle_state,
       machine.failure_report,
       connection.connection_state,
       coalesce(current_runtime.state_reason_code, '') AS connection_state_reason,
       coalesce(machine.lifecycle_reason_code, '') AS lifecycle_reason_code,
       machine.lifecycle_reason_message,
       coalesce(pool.name, '') AS machine_pool_name,
       (binding.state = 'attached' AND pmgrant.id IS NULL)::boolean AS project_grant_missing,
       coalesce((
         binding.state = 'attached'
         AND pmgrant.id IS NOT NULL
         AND machine.deleted_at IS NULL
         AND machine.lifecycle_state = 'active'
         AND connection.connection_state IN ('online', 'asleep')
       ), false)::boolean AS executable
FROM agent_machine_bindings binding
JOIN machines machine ON machine.org_id = binding.org_id
  AND machine.id = binding.machine_id
LEFT JOIN project_machine_grants pmgrant ON pmgrant.org_id = binding.org_id
  AND pmgrant.project_id = binding.project_id
  AND pmgrant.machine_id = binding.machine_id
JOIN machine_connection_states connection ON connection.org_id = machine.org_id
  AND connection.machine_id = machine.id
LEFT JOIN daemon_runtimes current_runtime ON current_runtime.org_id = machine.org_id
  AND current_runtime.id = machine.current_daemon_runtime_id
LEFT JOIN machine_pools pool ON pool.org_id = machine.org_id
  AND pool.id = machine.machine_pool_id
WHERE binding.project_id = sqlc.arg(project_id)
  AND binding.agent_id = sqlc.arg(agent_id)
  AND (sqlc.narg(machine_ref)::text IS NULL OR binding.machine_ref = sqlc.narg(machine_ref)::text)
  AND (
    binding.state = 'attached'
    OR (
      sqlc.arg(include_released_pool)::boolean
      AND sqlc.narg(machine_ref)::text IS NOT NULL
      AND binding.state = 'released'
      AND binding.binding_kind = 'pool'
      AND machine.source_kind = 'pool'
    )
  )
ORDER BY binding.created_at, binding.id;

-- name: ListAgentMachineBindings :many
SELECT binding.id, binding.org_id, binding.project_id, binding.agent_id,
       binding.create_tool_call_id, binding.delete_tool_call_id, binding.machine_id,
       binding.machine_ref, binding.binding_kind, binding.state, binding.description,
       binding.env_overlay, binding.secret_env_overlay, binding.metadata,
       binding.created_at, binding.updated_at,
       coalesce(nullif(binding.cwd, ''), machine.cwd, '') AS effective_cwd
FROM agent_machine_bindings binding
LEFT JOIN machines machine ON machine.org_id = binding.org_id
  AND machine.id = binding.machine_id
  AND machine.deleted_at IS NULL
WHERE binding.project_id = sqlc.arg(project_id)
  AND binding.agent_id = sqlc.arg(agent_id)
ORDER BY binding.created_at, binding.id;

-- name: ListAttachedMachineBindingOverlays :many
SELECT id, env_overlay, secret_env_overlay
FROM agent_machine_bindings
WHERE org_id = sqlc.arg(org_id)
  AND machine_id = sqlc.arg(machine_id)
  AND state = 'attached'
ORDER BY id;

-- name: ReleaseAgentMachineBindingsForMachine :exec
UPDATE agent_machine_bindings
SET state = 'released',
    updated_at = statement_timestamp()
WHERE org_id = sqlc.arg(org_id)
  AND machine_id = sqlc.arg(machine_id)
  AND state = 'attached';

-- name: ReleaseExplicitAgentMachineBindingsForAgent :exec
UPDATE agent_machine_bindings binding
SET state = 'released',
    updated_at = agent.archived_at
FROM agents agent
WHERE binding.project_id = sqlc.arg(project_id)
  AND binding.agent_id = sqlc.arg(agent_id)
  AND agent.org_id = binding.org_id
  AND agent.project_id = binding.project_id
  AND agent.id = binding.agent_id
  AND agent.state = 'archived'
  AND agent.archived_at IS NOT NULL
  AND binding.state = 'attached'
  AND binding.binding_kind = 'explicit';
