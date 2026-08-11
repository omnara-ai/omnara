-- +goose Up

-- Missing rows admit new managed work; rows materialize per-organization overrides.
CREATE TABLE org_managed_work_admission (
    org_id uuid PRIMARY KEY REFERENCES orgs(id) ON DELETE CASCADE,
    new_managed_work_allowed boolean NOT NULL
);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION sync_machine_online_interval_from_daemon_runtime() RETURNS trigger AS $$
DECLARE
    interval_end timestamptz;
BEGIN
    IF TG_OP = 'INSERT' THEN
        INSERT INTO machine_online_intervals(
            org_id,
            machine_id,
            daemon_runtime_id,
            started_at
        ) VALUES (
            NEW.org_id,
            NEW.machine_id,
            NEW.id,
            NEW.last_seen_at
        );
        RETURN NEW;
    END IF;

    IF OLD.state = 'ended' AND NEW.state = 'active' THEN
        INSERT INTO machine_online_intervals(
            org_id,
            machine_id,
            daemon_runtime_id,
            started_at
        ) VALUES (
            NEW.org_id,
            NEW.machine_id,
            NEW.id,
            NEW.last_seen_at
        );
        RETURN NEW;
    END IF;

    IF OLD.state = 'active' AND NEW.state = 'ended' THEN
        interval_end := LEAST(NEW.ended_at, OLD.lease_expires_at);
        UPDATE machine_online_intervals
        SET ended_at = GREATEST(
                machine_online_intervals.started_at,
                OLD.last_seen_at,
                interval_end
            ),
            end_reason_code = COALESCE(NULLIF(NEW.state_reason_code, ''), 'daemon_runtime_ended')
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
            end_reason_code = 'daemon_lease_expired'
        WHERE org_id = NEW.org_id
          AND machine_id = NEW.machine_id
          AND daemon_runtime_id = NEW.id
          AND ended_at IS NULL;

        INSERT INTO machine_online_intervals(
            org_id,
            machine_id,
            daemon_runtime_id,
            started_at
        ) VALUES (
            NEW.org_id,
            NEW.machine_id,
            NEW.id,
            NEW.last_seen_at
        );
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- Open intervals confirm only committed heartbeats; closed intervals settle through their lease-capped end.
CREATE OR REPLACE VIEW machine_online_interval_facts AS
SELECT online_interval.id,
       online_interval.org_id,
       online_interval.machine_id,
       online_interval.daemon_runtime_id,
       online_interval.started_at,
       online_interval.ended_at,
       online_interval.end_reason_code,
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
       ) AS observed_through,
       CASE
           WHEN online_interval.ended_at IS NULL
               THEN GREATEST(online_interval.started_at, runtime.last_seen_at)
           ELSE online_interval.ended_at
       END AS confirmed_through
FROM machine_online_intervals online_interval
JOIN daemon_runtimes runtime
  ON runtime.org_id = online_interval.org_id
 AND runtime.machine_id = online_interval.machine_id
 AND runtime.id = online_interval.daemon_runtime_id;

-- Supports bounded global keyset scans over provider-reported cost facts.
CREATE INDEX model_call_contexts_provider_cost_idx
    ON model_call_contexts(completed_at, id)
    WHERE provider_reported_cost_usd IS NOT NULL;
