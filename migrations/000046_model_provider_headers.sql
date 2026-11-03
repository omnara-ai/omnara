-- +goose Up

ALTER TABLE model_provider_configs
    ADD COLUMN headers jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(headers) = 'object'),
    ADD COLUMN secret_headers jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(secret_headers) = 'object');
