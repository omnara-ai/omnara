-- +goose Up

CREATE OR REPLACE VIEW machine_connection_states AS
SELECT machine.id AS machine_id,
       machine.org_id,
       CASE WHEN token.id IS NOT NULL
         AND runtime.lease_expires_at > statement_timestamp()
         AND machine.lifecycle_state = 'active'
         AND machine.deleted_at IS NULL THEN 'online'
       WHEN machine.asleep_since IS NOT NULL
         AND machine.lifecycle_state = 'active'
         AND machine.deleted_at IS NULL THEN 'asleep'
       ELSE 'offline' END AS connection_state
FROM machines machine
LEFT JOIN daemon_runtimes runtime ON runtime.org_id = machine.org_id
  AND runtime.machine_id = machine.id
  AND runtime.id = machine.current_daemon_runtime_id
  AND runtime.state = 'active'
LEFT JOIN machine_daemon_tokens token ON token.org_id = runtime.org_id
  AND token.machine_id = runtime.machine_id
  AND token.id = runtime.daemon_token_id
  AND token.revoked_at IS NULL;
