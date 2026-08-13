-- +goose Up

ALTER TABLE model_provider_configs DROP CONSTRAINT model_provider_configs_api_variant_check;
ALTER TABLE model_provider_configs ADD CONSTRAINT model_provider_configs_api_variant_check
    CHECK (api_variant IN ('default', 'openrouter', 'bedrock'));
