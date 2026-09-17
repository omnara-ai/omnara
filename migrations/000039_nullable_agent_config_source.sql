-- +goose Up

ALTER TABLE agent_configs
    ALTER COLUMN source DROP NOT NULL,
    ALTER COLUMN source_format DROP NOT NULL,
    ALTER COLUMN source_format DROP DEFAULT,
    ALTER COLUMN source_hash DROP NOT NULL,
    ADD CONSTRAINT agent_configs_source_presence CHECK (
        (source IS NULL AND source_format IS NULL AND source_hash IS NULL)
        OR (source IS NOT NULL AND source_format IS NOT NULL AND source_hash IS NOT NULL)
    ),
    DROP CONSTRAINT agent_configs_project_id_effective_definition_hash_source_f_key,
    ADD UNIQUE NULLS NOT DISTINCT (project_id, effective_definition_hash, source_format, source_hash);
