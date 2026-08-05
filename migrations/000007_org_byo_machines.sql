-- +goose Up

-- Daemon machine tokens and runtimes.

CREATE TABLE machine_daemon_tokens (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id uuid NOT NULL REFERENCES orgs(id),
    machine_id uuid NOT NULL,
    name text NOT NULL,
    token_hash text NOT NULL,
    created_by_user_id uuid REFERENCES users(id),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL,
    last_used_at timestamptz,
    revoked_at timestamptz,
    revoke_reason text NOT NULL DEFAULT '',
    UNIQUE (token_hash),
    UNIQUE (org_id, machine_id, id),
    CHECK (name <> ''),
    CHECK (jsonb_typeof(metadata) = 'object'),
    FOREIGN KEY (org_id, machine_id) REFERENCES machines(org_id, id)
);

CREATE INDEX machine_daemon_tokens_byo_created_idx
    ON machine_daemon_tokens(org_id, machine_id, created_at DESC, id DESC)
    WHERE created_by_user_id IS NOT NULL;

CREATE TABLE daemon_runtimes (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id uuid NOT NULL,
    machine_id uuid NOT NULL,
    daemon_token_id uuid NOT NULL,
    daemon_instance_id uuid NOT NULL,
    daemon_version text NOT NULL,
    state text NOT NULL,
    state_reason_code text,
    state_reason_message text NOT NULL DEFAULT '',
    capacity jsonb NOT NULL DEFAULT '{}'::jsonb,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL,
    lease_expires_at timestamptz NOT NULL,
    ended_at timestamptz,
    updated_at timestamptz NOT NULL,
    CHECK (state IN ('active', 'ended')),
    CHECK ((state = 'active') = (ended_at IS NULL)),
    CHECK (lease_expires_at > last_seen_at),
    CHECK (daemon_version <> ''),
    CHECK (jsonb_typeof(capacity) = 'object'),
    CHECK (jsonb_typeof(metadata) = 'object'),
    FOREIGN KEY (org_id, machine_id) REFERENCES machines(org_id, id),
    FOREIGN KEY (org_id, machine_id, daemon_token_id) REFERENCES machine_daemon_tokens(org_id, machine_id, id),
    UNIQUE (org_id, machine_id, id),
    UNIQUE (org_id, machine_id, daemon_instance_id)
);

CREATE UNIQUE INDEX daemon_runtimes_one_active_idx
    ON daemon_runtimes(org_id, machine_id)
    WHERE state = 'active';

CREATE INDEX daemon_runtimes_active_lease_expiry_idx
    ON daemon_runtimes(lease_expires_at, id)
    INCLUDE (org_id, machine_id)
    WHERE state = 'active';

CREATE INDEX daemon_runtimes_machine_recent_idx
    ON daemon_runtimes(org_id, machine_id, (coalesce(ended_at, lease_expires_at)) DESC, id DESC);

ALTER TABLE machines ADD FOREIGN KEY (org_id, id, current_daemon_runtime_id)
    REFERENCES daemon_runtimes(org_id, machine_id, id);

CREATE VIEW reportable_daemon_runtimes AS
SELECT runtime.id,
       runtime.org_id,
       runtime.machine_id,
       runtime.daemon_token_id,
       runtime.daemon_instance_id,
       runtime.daemon_version,
       runtime.state,
       runtime.lease_expires_at
FROM daemon_runtimes runtime
JOIN machines machine ON machine.org_id = runtime.org_id
  AND machine.id = runtime.machine_id
JOIN machine_daemon_tokens token ON token.org_id = runtime.org_id
  AND token.machine_id = runtime.machine_id
  AND token.id = runtime.daemon_token_id
WHERE machine.current_daemon_runtime_id = runtime.id
  AND machine.lifecycle_state = 'active'
  AND machine.deleted_at IS NULL
  AND token.revoked_at IS NULL
  AND (
    runtime.state = 'active'
    OR (
      runtime.state = 'ended'
      AND runtime.state_reason_code = 'daemon_lease_expired'
    )
  );

CREATE VIEW registered_daemon_runtimes AS
SELECT runtime.id,
       runtime.org_id,
       runtime.machine_id,
       runtime.daemon_token_id,
       runtime.daemon_instance_id,
       runtime.daemon_version,
       runtime.lease_expires_at
FROM reportable_daemon_runtimes runtime
WHERE runtime.state = 'active';

CREATE VIEW online_daemon_runtimes AS
SELECT runtime.id,
       runtime.org_id,
       runtime.machine_id,
       runtime.daemon_token_id,
       runtime.daemon_instance_id
FROM registered_daemon_runtimes runtime
WHERE runtime.lease_expires_at > statement_timestamp();

CREATE VIEW machine_connection_states AS
SELECT machine.id AS machine_id,
       machine.org_id,
       CASE WHEN EXISTS (
         SELECT 1
         FROM online_daemon_runtimes runtime
         WHERE runtime.org_id = machine.org_id
           AND runtime.machine_id = machine.id
       ) THEN 'online'
       WHEN machine.asleep_since IS NOT NULL
         AND machine.lifecycle_state = 'active'
         AND machine.deleted_at IS NULL THEN 'asleep'
       ELSE 'offline' END AS connection_state
FROM machines machine;
