-- +goose Up

ALTER TABLE agent_configs DROP COLUMN definition;
