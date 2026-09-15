-- +goose Up

ALTER TABLE agent_machine_bindings DROP COLUMN machine_ref;
