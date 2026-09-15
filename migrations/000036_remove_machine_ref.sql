-- +goose Up

-- Stop old API, worker, and maintenance instances before applying this migration.
-- Their binding queries still read/write machine_ref, so mixed-version rollout
-- and rollback to those binaries are not supported after the column is removed.
ALTER TABLE agent_machine_bindings DROP COLUMN machine_ref;
