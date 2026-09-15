-- +goose Up

ALTER TABLE agent_machine_bindings ALTER COLUMN machine_ref DROP NOT NULL;
