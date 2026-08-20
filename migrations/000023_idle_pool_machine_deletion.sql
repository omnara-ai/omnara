-- +goose Up

ALTER TABLE machine_pools
    ADD COLUMN delete_after_idle_minutes integer,
    ADD CONSTRAINT machine_pools_delete_after_idle_minutes_check
        CHECK (delete_after_idle_minutes IS NULL OR delete_after_idle_minutes >= 5);

ALTER TABLE project_machine_pool_grants
    ADD COLUMN delete_after_idle_minutes integer,
    ADD CONSTRAINT project_machine_pool_grants_delete_after_idle_minutes_check
        CHECK (delete_after_idle_minutes IS NULL OR delete_after_idle_minutes = 0 OR delete_after_idle_minutes >= 5);

ALTER TABLE agent_machine_bindings
    ADD COLUMN delete_after_idle_minutes integer,
    ADD CONSTRAINT agent_machine_bindings_delete_after_idle_minutes_check
        CHECK (delete_after_idle_minutes IS NULL OR delete_after_idle_minutes = 0 OR delete_after_idle_minutes >= 5);

CREATE UNIQUE INDEX agent_machine_bindings_one_attached_pool_machine_idx
    ON agent_machine_bindings(org_id, machine_id)
    WHERE binding_kind = 'pool' AND state = 'attached';

-- Tracks activity that should postpone pool-machine idle deletion, not every process row update.
ALTER TABLE processes
    ADD COLUMN last_activity_at timestamptz NOT NULL DEFAULT statement_timestamp();

CREATE INDEX processes_machine_last_activity_idx
    ON processes(org_id, machine_id, last_activity_at DESC);

CREATE VIEW expired_idle_pool_machine_candidates AS
SELECT machine.org_id, machine.id AS machine_id
FROM machines machine
JOIN machine_pools pool ON pool.org_id = machine.org_id
    AND pool.id = machine.machine_pool_id
    AND pool.deleted_at IS NULL
JOIN project_machine_grants machine_grant ON machine_grant.org_id = machine.org_id
    AND machine_grant.machine_id = machine.id
    AND machine_grant.source_kind = 'pool'
JOIN project_machine_pool_grants pool_grant ON pool_grant.org_id = machine_grant.org_id
    AND pool_grant.project_id = machine_grant.project_id
    AND pool_grant.id = machine_grant.project_machine_pool_grant_id
    AND pool_grant.machine_pool_id = pool.id
JOIN agent_machine_bindings binding ON binding.org_id = machine.org_id
    AND binding.project_id = machine_grant.project_id
    AND binding.machine_id = machine.id
    AND binding.binding_kind = 'pool'
    AND binding.state = 'attached'
LEFT JOIN LATERAL (
    SELECT process.last_activity_at
    FROM processes process
    WHERE process.org_id = machine.org_id
        AND process.machine_id = machine.id
    ORDER BY process.last_activity_at DESC
    LIMIT 1
) activity ON true
WHERE machine.source_kind = 'pool'
    AND machine.lifecycle_state = 'active'
    AND machine.deleted_at IS NULL
    AND machine.last_observed_at IS NOT NULL
    AND (
        machine.wake_attempt_expires_at IS NULL
        OR machine.wake_attempt_expires_at <= statement_timestamp()
    )
    AND coalesce(binding.delete_after_idle_minutes, pool_grant.delete_after_idle_minutes, pool.delete_after_idle_minutes)
        >= 5
    AND machine.lifecycle_changed_at <= statement_timestamp() - make_interval(mins => coalesce(
        binding.delete_after_idle_minutes,
        pool_grant.delete_after_idle_minutes,
        pool.delete_after_idle_minutes
    ))
    AND greatest(machine.lifecycle_changed_at, coalesce(activity.last_activity_at, machine.lifecycle_changed_at))
        <= statement_timestamp() - make_interval(mins => coalesce(
            binding.delete_after_idle_minutes,
            pool_grant.delete_after_idle_minutes,
            pool.delete_after_idle_minutes
        ))
    AND NOT EXISTS (
        SELECT 1
        FROM processes process
        WHERE process.org_id = machine.org_id
            AND process.machine_id = machine.id
            AND process.state = 'queued'
    )
    AND NOT EXISTS (
        SELECT 1
        FROM processes process
        WHERE process.org_id = machine.org_id
            AND process.machine_id = machine.id
            AND process.state IN ('starting', 'running')
    )
    AND NOT EXISTS (
        SELECT 1
        FROM processes process
        JOIN process_actions action ON action.org_id = process.org_id
            AND action.project_id = process.project_id
            AND action.agent_id = process.agent_id
            AND action.process_id = process.id
            AND action.state IN ('queued', 'accepted')
        WHERE process.org_id = machine.org_id
            AND process.machine_id = machine.id
    );
