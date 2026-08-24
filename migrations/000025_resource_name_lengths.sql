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

-- Unicode Cc, Cf, Other_Default_Ignorable_Code_Point, Variation_Selector, U+2800, U+FFFD,
-- and White_Space except U+0020, kept in sync with Go's unicode tables.
-- +goose StatementBegin
CREATE FUNCTION resource_name_codepoint_is_forbidden_v1(codepoint integer) RETURNS boolean AS $$
    SELECT codepoint BETWEEN 0 AND 31
        OR codepoint BETWEEN 127 AND 160
        OR codepoint = 173
        OR codepoint = 847
        OR codepoint BETWEEN 1536 AND 1541
        OR codepoint = 1564
        OR codepoint = 1757
        OR codepoint = 1807
        OR codepoint BETWEEN 2192 AND 2193
        OR codepoint = 2274
        OR codepoint BETWEEN 4447 AND 4448
        OR codepoint = 5760
        OR codepoint BETWEEN 6068 AND 6069
        OR codepoint BETWEEN 6155 AND 6159
        OR codepoint BETWEEN 8192 AND 8207
        OR codepoint BETWEEN 8232 AND 8239
        OR codepoint BETWEEN 8287 AND 8303
        OR codepoint = 10240
        OR codepoint = 12288
        OR codepoint = 12644
        OR codepoint BETWEEN 65024 AND 65039
        OR codepoint = 65279
        OR codepoint = 65440
        OR codepoint BETWEEN 65520 AND 65531
        OR codepoint = 65533
        OR codepoint = 69821
        OR codepoint = 69837
        OR codepoint BETWEEN 78896 AND 78911
        OR codepoint BETWEEN 113824 AND 113827
        OR codepoint BETWEEN 119155 AND 119162
        OR codepoint BETWEEN 917504 AND 921599;
$$ LANGUAGE sql IMMUTABLE;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION resource_name_is_valid_v1(candidate text, allow_empty boolean) RETURNS boolean
LANGUAGE sql IMMUTABLE
BEGIN ATOMIC
    SELECT candidate IS NOT NULL
        AND (allow_empty OR candidate <> '')
        AND candidate IS NFC NORMALIZED
        AND char_length(candidate) <= 64
        AND candidate = btrim(candidate, ' ')
        AND NOT EXISTS (
            SELECT 1
            FROM generate_series(
                1,
                least(char_length(candidate), 64)
            ) AS positions(position)
            WHERE resource_name_codepoint_is_forbidden_v1(
                ascii(substr(candidate, positions.position, 1))
            )
        );
END;
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
  AND resource_name_is_valid_v1(resource_name_repair(name), false);

UPDATE projects
SET name = resource_name_repair(name)
WHERE name IS DISTINCT FROM resource_name_repair(name)
  AND resource_name_is_valid_v1(resource_name_repair(name), false);

UPDATE personal_access_tokens
SET name = resource_name_repair(name)
WHERE name IS DISTINCT FROM resource_name_repair(name)
  AND resource_name_is_valid_v1(resource_name_repair(name), false);

UPDATE auth_device_flows
SET client_name = resource_name_repair(client_name)
WHERE client_name IS DISTINCT FROM resource_name_repair(client_name)
  AND resource_name_is_valid_v1(resource_name_repair(client_name), false);

UPDATE auth_device_flows
SET token_name = resource_name_repair(token_name)
WHERE token_name IS DISTINCT FROM resource_name_repair(token_name)
  AND resource_name_is_valid_v1(resource_name_repair(token_name), false);

UPDATE org_api_keys
SET name = resource_name_repair(name)
WHERE name IS DISTINCT FROM resource_name_repair(name)
  AND resource_name_is_valid_v1(resource_name_repair(name), false);

UPDATE secrets
SET name = resource_name_repair(name)
WHERE name IS DISTINCT FROM resource_name_repair(name)
  AND resource_name_is_valid_v1(resource_name_repair(name), false);

UPDATE model_provider_configs
SET name = resource_name_repair(name)
WHERE name IS DISTINCT FROM resource_name_repair(name)
  AND resource_name_is_valid_v1(resource_name_repair(name), false);

UPDATE configured_models
SET name = resource_name_repair(name)
WHERE name IS DISTINCT FROM resource_name_repair(name)
  AND resource_name_is_valid_v1(resource_name_repair(name), false);

UPDATE agent_profiles
SET name = resource_name_repair(name)
WHERE name IS DISTINCT FROM resource_name_repair(name)
  AND resource_name_is_valid_v1(resource_name_repair(name), false);

UPDATE agents
SET name = resource_name_repair(name)
WHERE name IS DISTINCT FROM resource_name_repair(name)
  AND resource_name_is_valid_v1(resource_name_repair(name), true);

UPDATE machine_pools
SET name = resource_name_repair(name)
WHERE name IS DISTINCT FROM resource_name_repair(name)
  AND resource_name_is_valid_v1(resource_name_repair(name), false);

UPDATE machines
SET display_name = resource_name_repair(display_name)
WHERE display_name IS DISTINCT FROM resource_name_repair(display_name)
  AND resource_name_is_valid_v1(resource_name_repair(display_name), false);

UPDATE machine_daemon_tokens
SET name = resource_name_repair(name)
WHERE name IS DISTINCT FROM resource_name_repair(name)
  AND resource_name_is_valid_v1(resource_name_repair(name), false);

UPDATE cron_triggers
SET name = resource_name_repair(name)
WHERE name IS DISTINCT FROM resource_name_repair(name)
  AND resource_name_is_valid_v1(resource_name_repair(name), false);

DROP FUNCTION resource_name_repair(text);

ALTER TABLE orgs ADD CONSTRAINT orgs_name_policy CHECK (resource_name_is_valid_v1(name, false));
ALTER TABLE projects ADD CONSTRAINT projects_name_policy CHECK (resource_name_is_valid_v1(name, false));
ALTER TABLE personal_access_tokens ADD CONSTRAINT personal_access_tokens_name_policy CHECK (resource_name_is_valid_v1(name, false));
ALTER TABLE auth_device_flows ADD CONSTRAINT auth_device_flows_client_name_policy CHECK (resource_name_is_valid_v1(client_name, false));
ALTER TABLE auth_device_flows ADD CONSTRAINT auth_device_flows_token_name_policy CHECK (resource_name_is_valid_v1(token_name, false));
ALTER TABLE org_api_keys ADD CONSTRAINT org_api_keys_name_policy CHECK (resource_name_is_valid_v1(name, false));
ALTER TABLE secrets ADD CONSTRAINT secrets_name_policy CHECK (resource_name_is_valid_v1(name, false));
ALTER TABLE model_provider_configs ADD CONSTRAINT model_provider_configs_name_policy CHECK (resource_name_is_valid_v1(name, false));
ALTER TABLE configured_models ADD CONSTRAINT configured_models_name_policy CHECK (resource_name_is_valid_v1(name, false));
ALTER TABLE agent_profiles ADD CONSTRAINT agent_profiles_name_policy CHECK (resource_name_is_valid_v1(name, false));
ALTER TABLE agents ADD CONSTRAINT agents_name_policy CHECK (resource_name_is_valid_v1(name, true));
ALTER TABLE machine_pools ADD CONSTRAINT machine_pools_name_policy CHECK (resource_name_is_valid_v1(name, false));
ALTER TABLE machines ADD CONSTRAINT machines_display_name_policy CHECK (resource_name_is_valid_v1(display_name, false));
ALTER TABLE machines ALTER COLUMN display_name DROP DEFAULT;
ALTER TABLE machine_daemon_tokens ADD CONSTRAINT machine_daemon_tokens_name_policy CHECK (resource_name_is_valid_v1(name, false));
ALTER TABLE cron_triggers ADD CONSTRAINT cron_triggers_name_policy CHECK (resource_name_is_valid_v1(name, false));

-- +goose StatementBegin
CREATE FUNCTION skill_name_is_valid_v1(candidate text) RETURNS boolean AS $$
    SELECT candidate IS NOT NULL
        AND char_length(candidate) BETWEEN 1 AND 64
        AND candidate ~ '^[a-z0-9]+(-[a-z0-9]+)*$';
$$ LANGUAGE sql IMMUTABLE;
-- +goose StatementEnd

ALTER TABLE skills ADD CONSTRAINT skills_name_policy CHECK (skill_name_is_valid_v1(name));
