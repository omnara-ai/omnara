-- +goose Up

ALTER TABLE mcp_server_catalogs
    ADD COLUMN refresh_error text NOT NULL DEFAULT '';
