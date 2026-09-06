-- +goose Up

ALTER TABLE model_provider_configs
    ALTER COLUMN request_timeout_ms SET DEFAULT 3600000,
    ADD COLUMN idle_timeout_ms integer NOT NULL DEFAULT 300000
        CHECK (idle_timeout_ms > 0);
