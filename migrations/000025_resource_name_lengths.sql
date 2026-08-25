-- +goose Up

-- +goose StatementBegin
DO $$
BEGIN
    IF current_setting('server_encoding') <> 'UTF8' THEN
        RAISE EXCEPTION 'PostgreSQL UTF8 database encoding is required (server_encoding=%)',
            current_setting('server_encoding')
            USING ERRCODE = 'feature_not_supported';
    END IF;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION resource_name_storage_is_valid(candidate text) RETURNS boolean
LANGUAGE sql IMMUTABLE
RETURN candidate IS NOT NULL
    AND candidate <> ''
    AND candidate IS NFC NORMALIZED
    AND char_length(candidate) <= 64
    AND candidate = btrim(candidate, ' ')
    AND (candidate COLLATE "pg_unicode_fast") !~ '[[:cntrl:]]'
    AND (replace(candidate, ' ', '') COLLATE "pg_unicode_fast") !~ '[[:space:]]'
    AND strpos(candidate, U&'\FFFD') = 0;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION resource_name_repair(candidate text) RETURNS text AS $$
    WITH whitespace(characters) AS (
        VALUES (U&'\0009\000A\000B\000C\000D\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000')
    )
    SELECT btrim(
        left(normalize(btrim(candidate, characters), NFC), 64),
        characters
    )
    FROM whitespace;
$$ LANGUAGE sql IMMUTABLE;
-- +goose StatementEnd

-- Run this hard-cutover migration with application writers stopped.
UPDATE orgs
SET name = resource_name_repair(name)
WHERE name IS DISTINCT FROM resource_name_repair(name)
  AND resource_name_storage_is_valid(resource_name_repair(name));

UPDATE agents
SET name = resource_name_repair(name)
WHERE name IS DISTINCT FROM resource_name_repair(name)
  AND (
      resource_name_repair(name) = ''
      OR resource_name_storage_is_valid(resource_name_repair(name))
  );

DROP FUNCTION resource_name_repair(text);

ALTER TABLE orgs ADD CONSTRAINT orgs_name_policy CHECK (resource_name_storage_is_valid(name));
ALTER TABLE projects ADD CONSTRAINT projects_name_policy CHECK (resource_name_storage_is_valid(name));
ALTER TABLE personal_access_tokens
    DROP CONSTRAINT personal_access_tokens_name_check,
    ADD CONSTRAINT personal_access_tokens_name_policy CHECK (resource_name_storage_is_valid(name));
ALTER TABLE auth_device_flows
    DROP CONSTRAINT auth_device_flows_client_name_check,
    DROP CONSTRAINT auth_device_flows_token_name_check,
    ADD CONSTRAINT auth_device_flows_client_name_policy CHECK (resource_name_storage_is_valid(client_name)),
    ADD CONSTRAINT auth_device_flows_token_name_policy CHECK (resource_name_storage_is_valid(token_name));
ALTER TABLE org_api_keys
    DROP CONSTRAINT org_api_keys_name_check,
    ADD CONSTRAINT org_api_keys_name_policy CHECK (resource_name_storage_is_valid(name));
ALTER TABLE secrets
    DROP CONSTRAINT secrets_name_check,
    ADD CONSTRAINT secrets_name_policy CHECK (resource_name_storage_is_valid(name));
ALTER TABLE model_provider_configs
    DROP CONSTRAINT model_provider_configs_name_check,
    ADD CONSTRAINT model_provider_configs_name_policy CHECK (resource_name_storage_is_valid(name));
ALTER TABLE configured_models
    DROP CONSTRAINT configured_models_name_check,
    ADD CONSTRAINT configured_models_name_policy CHECK (resource_name_storage_is_valid(name));
ALTER TABLE agent_profiles
    DROP CONSTRAINT agent_profiles_name_check,
    ADD CONSTRAINT agent_profiles_name_policy CHECK (resource_name_storage_is_valid(name));
ALTER TABLE agents ADD CONSTRAINT agents_name_policy CHECK (name = '' OR resource_name_storage_is_valid(name));
ALTER TABLE machine_pools
    DROP CONSTRAINT machine_pools_name_check,
    ADD CONSTRAINT machine_pools_name_policy CHECK (resource_name_storage_is_valid(name));
ALTER TABLE machines
    ADD CONSTRAINT machines_display_name_policy CHECK (resource_name_storage_is_valid(display_name)),
    ALTER COLUMN display_name DROP DEFAULT;
ALTER TABLE machine_daemon_tokens
    DROP CONSTRAINT machine_daemon_tokens_name_check,
    ADD CONSTRAINT machine_daemon_tokens_name_policy CHECK (resource_name_storage_is_valid(name));
ALTER TABLE cron_triggers
    DROP CONSTRAINT cron_triggers_name_check,
    ADD CONSTRAINT cron_triggers_name_policy CHECK (resource_name_storage_is_valid(name));

-- +goose StatementBegin
CREATE FUNCTION skill_name_is_valid(candidate text) RETURNS boolean AS $$
    SELECT candidate IS NOT NULL
        AND char_length(candidate) BETWEEN 1 AND 64
        AND (candidate COLLATE "C") ~ '^[a-z0-9]+(-[a-z0-9]+)*$';
$$ LANGUAGE sql IMMUTABLE;
-- +goose StatementEnd

ALTER TABLE skills
    DROP CONSTRAINT skills_name_check,
    ADD CONSTRAINT skills_name_policy CHECK (skill_name_is_valid(name));
