-- +goose Up

-- Provider runtime protection and neutral daemon-online interval facts.

ALTER TABLE machine_pools
    ADD COLUMN runtime_protection_enabled boolean NOT NULL DEFAULT true;

ALTER TABLE machines
    ADD COLUMN provider_runtime_mismatch_since timestamptz;

CREATE INDEX machines_provider_runtime_mismatch_due_idx
    ON machines(provider_runtime_mismatch_since, id)
    WHERE provider_runtime_mismatch_since IS NOT NULL
      AND lifecycle_state = 'active'
      AND deleted_at IS NULL;

CREATE TABLE machine_online_intervals (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id uuid NOT NULL,
    machine_id uuid NOT NULL,
    daemon_runtime_id uuid NOT NULL,
    started_at timestamptz NOT NULL,
    ended_at timestamptz,
    start_reason_code text NOT NULL,
    end_reason_code text,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK (start_reason_code IN (
        'daemon_runtime_registered',
        'daemon_runtime_revived',
        'daemon_lease_renewed_after_expiry',
        'migration_cutover'
    )),
    CHECK (end_reason_code IS NULL OR end_reason_code <> ''),
    CHECK ((ended_at IS NULL) = (end_reason_code IS NULL)),
    CHECK (ended_at IS NULL OR ended_at >= started_at),
    FOREIGN KEY (org_id, machine_id) REFERENCES machines(org_id, id),
    FOREIGN KEY (org_id, machine_id, daemon_runtime_id)
        REFERENCES daemon_runtimes(org_id, machine_id, id)
);

CREATE UNIQUE INDEX machine_online_intervals_one_open_per_machine_idx
    ON machine_online_intervals(org_id, machine_id)
    WHERE ended_at IS NULL;

CREATE INDEX machine_online_intervals_org_started_idx
    ON machine_online_intervals(org_id, started_at, id);

CREATE INDEX machine_online_intervals_machine_started_idx
    ON machine_online_intervals(org_id, machine_id, started_at, id);

-- +goose StatementBegin
CREATE FUNCTION sync_machine_online_interval_from_daemon_runtime() RETURNS trigger AS $$
DECLARE
    interval_end timestamptz;
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.state = 'active' THEN
            INSERT INTO machine_online_intervals(
                org_id,
                machine_id,
                daemon_runtime_id,
                started_at,
                start_reason_code,
                created_at,
                updated_at
            ) VALUES (
                NEW.org_id,
                NEW.machine_id,
                NEW.id,
                NEW.last_seen_at,
                'daemon_runtime_registered',
                statement_timestamp(),
                statement_timestamp()
            );
        END IF;
        RETURN NEW;
    END IF;

    IF OLD.state = 'ended' AND NEW.state = 'active' THEN
        INSERT INTO machine_online_intervals(
            org_id,
            machine_id,
            daemon_runtime_id,
            started_at,
            start_reason_code,
            created_at,
            updated_at
        ) VALUES (
            NEW.org_id,
            NEW.machine_id,
            NEW.id,
            NEW.last_seen_at,
            'daemon_runtime_revived',
            statement_timestamp(),
            statement_timestamp()
        );
        RETURN NEW;
    END IF;

    IF OLD.state = 'active' AND NEW.state = 'ended' THEN
        interval_end := LEAST(NEW.ended_at, OLD.lease_expires_at);
        UPDATE machine_online_intervals
        SET ended_at = GREATEST(machine_online_intervals.started_at, interval_end),
            end_reason_code = COALESCE(NULLIF(NEW.state_reason_code, ''), 'daemon_runtime_ended'),
            updated_at = statement_timestamp()
        WHERE org_id = NEW.org_id
          AND machine_id = NEW.machine_id
          AND daemon_runtime_id = NEW.id
          AND ended_at IS NULL;
        RETURN NEW;
    END IF;

    IF OLD.state = 'active'
       AND NEW.state = 'active'
       AND OLD.lease_expires_at <= NEW.last_seen_at THEN
        UPDATE machine_online_intervals
        SET ended_at = GREATEST(machine_online_intervals.started_at, OLD.lease_expires_at),
            end_reason_code = 'daemon_lease_expired',
            updated_at = statement_timestamp()
        WHERE org_id = NEW.org_id
          AND machine_id = NEW.machine_id
          AND daemon_runtime_id = NEW.id
          AND ended_at IS NULL;

        INSERT INTO machine_online_intervals(
            org_id,
            machine_id,
            daemon_runtime_id,
            started_at,
            start_reason_code,
            created_at,
            updated_at
        ) VALUES (
            NEW.org_id,
            NEW.machine_id,
            NEW.id,
            NEW.last_seen_at,
            'daemon_lease_renewed_after_expiry',
            statement_timestamp(),
            statement_timestamp()
        );
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER daemon_runtimes_open_machine_online_interval
    AFTER INSERT
    ON daemon_runtimes
    FOR EACH ROW
    WHEN (NEW.state = 'active')
    EXECUTE FUNCTION sync_machine_online_interval_from_daemon_runtime();

CREATE TRIGGER daemon_runtimes_update_machine_online_interval
    AFTER UPDATE OF state, last_seen_at, lease_expires_at, ended_at
    ON daemon_runtimes
    FOR EACH ROW
    WHEN (
        (OLD.state = 'ended' AND NEW.state = 'active')
        OR (OLD.state = 'active' AND NEW.state = 'ended')
        OR (
            OLD.state = 'active'
            AND NEW.state = 'active'
            AND OLD.lease_expires_at <= NEW.last_seen_at
        )
    )
    EXECUTE FUNCTION sync_machine_online_interval_from_daemon_runtime();

-- +goose StatementBegin
CREATE FUNCTION machine_online_intervals_reject_mutation() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'machine online intervals cannot be deleted'
            USING ERRCODE = '25006';
    END IF;
    IF OLD.ended_at IS NOT NULL
       OR NEW.id IS DISTINCT FROM OLD.id
       OR NEW.org_id IS DISTINCT FROM OLD.org_id
       OR NEW.machine_id IS DISTINCT FROM OLD.machine_id
       OR NEW.daemon_runtime_id IS DISTINCT FROM OLD.daemon_runtime_id
       OR NEW.started_at IS DISTINCT FROM OLD.started_at
       OR NEW.start_reason_code IS DISTINCT FROM OLD.start_reason_code
       OR NEW.created_at IS DISTINCT FROM OLD.created_at
       OR NEW.ended_at IS NULL
       OR NEW.end_reason_code IS NULL THEN
        RAISE EXCEPTION 'machine online intervals are append-only'
            USING ERRCODE = '25006';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER machine_online_intervals_append_only
    BEFORE UPDATE OR DELETE ON machine_online_intervals
    FOR EACH ROW
    EXECUTE FUNCTION machine_online_intervals_reject_mutation();

WITH cutover AS (
    SELECT statement_timestamp() AS observed_at
)
INSERT INTO machine_online_intervals(
    org_id,
    machine_id,
    daemon_runtime_id,
    started_at,
    start_reason_code,
    created_at,
    updated_at
)
SELECT runtime.org_id,
       runtime.machine_id,
       runtime.id,
       cutover.observed_at,
       'migration_cutover',
       cutover.observed_at,
       cutover.observed_at
FROM online_daemon_runtimes runtime
CROSS JOIN cutover;

CREATE VIEW machine_online_interval_facts AS
SELECT online_interval.id,
       online_interval.org_id,
       online_interval.machine_id,
       online_interval.daemon_runtime_id,
       online_interval.started_at,
       online_interval.ended_at,
       online_interval.start_reason_code,
       online_interval.end_reason_code,
       online_interval.created_at,
       online_interval.updated_at,
       GREATEST(
           online_interval.started_at,
           COALESCE(online_interval.ended_at, runtime.lease_expires_at)
       ) AS effective_end_at,
       GREATEST(
           online_interval.started_at,
           LEAST(
               statement_timestamp(),
               GREATEST(
                   online_interval.started_at,
                   COALESCE(online_interval.ended_at, runtime.lease_expires_at)
               )
           )
       ) AS observed_through
FROM machine_online_intervals online_interval
JOIN daemon_runtimes runtime
  ON runtime.org_id = online_interval.org_id
 AND runtime.machine_id = online_interval.machine_id
 AND runtime.id = online_interval.daemon_runtime_id;
