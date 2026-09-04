-- +goose Up

ALTER TABLE model_provider_configs DROP CONSTRAINT model_provider_configs_auth_kind_check;
ALTER TABLE model_provider_configs ADD CONSTRAINT model_provider_configs_auth_kind_check
    CHECK (auth_kind IN ('bearer_token', 'api_key_header', 'sigv4'));
