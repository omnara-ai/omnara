-- +goose Up

ALTER TABLE machine_pools
    ADD COLUMN delete_after_idle_minutes integer,
    ADD CONSTRAINT machine_pools_delete_after_idle_minutes_check
        CHECK (delete_after_idle_minutes IS NULL OR delete_after_idle_minutes > 0);

ALTER TABLE project_machine_pool_grants
    ADD COLUMN delete_after_idle_minutes integer,
    ADD CONSTRAINT project_machine_pool_grants_delete_after_idle_minutes_check
        CHECK (delete_after_idle_minutes IS NULL OR delete_after_idle_minutes > 0);

ALTER TABLE agent_machine_bindings
    ADD COLUMN delete_after_idle_minutes integer,
    ADD CONSTRAINT agent_machine_bindings_delete_after_idle_minutes_check
        CHECK (delete_after_idle_minutes IS NULL OR delete_after_idle_minutes > 0);

WITH latest_applied_action AS (
    SELECT process.id AS process_id,
           max(action.updated_at) AS updated_at
    FROM processes process
    JOIN machines machine ON machine.org_id = process.org_id
        AND machine.id = process.machine_id
        AND machine.source_kind = 'pool'
        AND machine.deleted_at IS NULL
    JOIN process_actions action ON action.process_id = process.id
        AND action.state = 'applied'
    GROUP BY process.id
)
UPDATE processes process
SET updated_at = latest_applied_action.updated_at
FROM latest_applied_action
WHERE process.id = latest_applied_action.process_id
  AND process.updated_at < latest_applied_action.updated_at;

CREATE INDEX processes_machine_updated_idx
    ON processes(org_id, machine_id, updated_at DESC);
