-- +goose Up

ALTER TABLE agent_configs DROP COLUMN definition, DROP COLUMN compiler_version;
