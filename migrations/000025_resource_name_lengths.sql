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
CREATE FUNCTION resource_name_codepoint_is_forbidden(codepoint integer) RETURNS boolean AS $$
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
CREATE FUNCTION resource_name_is_valid_with_max(
    candidate text,
    allow_empty boolean,
    max_code_points integer
) RETURNS boolean
LANGUAGE sql IMMUTABLE
BEGIN ATOMIC
    SELECT candidate IS NOT NULL
        AND (allow_empty OR candidate <> '')
        AND max_code_points IS NOT NULL
        AND max_code_points > 0
        AND char_length(candidate) <= max_code_points
        AND octet_length(candidate) <= 4 * max_code_points
        AND candidate = btrim(candidate, ' ')
        AND NOT EXISTS (
            SELECT 1
            FROM generate_series(
                1,
                least(char_length(candidate), max_code_points)
            ) AS positions(position)
            WHERE resource_name_codepoint_is_forbidden(
                ascii(substr(candidate, positions.position, 1))
            )
        );
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION resource_name_is_valid(candidate text, allow_empty boolean) RETURNS boolean
LANGUAGE sql IMMUTABLE
BEGIN ATOMIC
    SELECT resource_name_is_valid_with_max(candidate, allow_empty, 64);
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION resource_name_repair(candidate text, max_code_points integer) RETURNS text AS $$
    WITH whitespace(characters) AS (
        VALUES (U&'\0009\000A\000B\000C\000D\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000')
    )
    SELECT btrim(left(btrim(candidate, characters), max_code_points), characters)
    FROM whitespace;
$$ LANGUAGE sql IMMUTABLE;
-- +goose StatementEnd

-- This one-time repair matches Go strings.TrimSpace, truncates by code point, then trims again.
-- Only fully valid results are written; run the hard-cutover migration with application writers stopped.
UPDATE orgs
SET name = resource_name_repair(name, 64)
WHERE name IS DISTINCT FROM resource_name_repair(name, 64)
  AND resource_name_is_valid(resource_name_repair(name, 64), false);

UPDATE projects
SET name = resource_name_repair(name, 64)
WHERE name IS DISTINCT FROM resource_name_repair(name, 64)
  AND resource_name_is_valid(resource_name_repair(name, 64), false);

UPDATE personal_access_tokens
SET name = resource_name_repair(name, 64)
WHERE name IS DISTINCT FROM resource_name_repair(name, 64)
  AND resource_name_is_valid(resource_name_repair(name, 64), false);

UPDATE auth_device_flows
SET client_name = resource_name_repair(client_name, 128)
WHERE client_name IS DISTINCT FROM resource_name_repair(client_name, 128)
  AND resource_name_is_valid_with_max(resource_name_repair(client_name, 128), false, 128);

UPDATE auth_device_flows
SET token_name = resource_name_repair(token_name, 64)
WHERE token_name IS DISTINCT FROM resource_name_repair(token_name, 64)
  AND resource_name_is_valid(resource_name_repair(token_name, 64), false);

UPDATE org_api_keys
SET name = resource_name_repair(name, 64)
WHERE name IS DISTINCT FROM resource_name_repair(name, 64)
  AND resource_name_is_valid(resource_name_repair(name, 64), false);

UPDATE secrets
SET name = resource_name_repair(name, 64)
WHERE name IS DISTINCT FROM resource_name_repair(name, 64)
  AND resource_name_is_valid(resource_name_repair(name, 64), false);

UPDATE model_provider_configs
SET name = resource_name_repair(name, 64)
WHERE name IS DISTINCT FROM resource_name_repair(name, 64)
  AND resource_name_is_valid(resource_name_repair(name, 64), false);

UPDATE configured_models
SET name = resource_name_repair(name, 64)
WHERE name IS DISTINCT FROM resource_name_repair(name, 64)
  AND resource_name_is_valid(resource_name_repair(name, 64), false);

UPDATE agent_profiles
SET name = resource_name_repair(name, 64)
WHERE name IS DISTINCT FROM resource_name_repair(name, 64)
  AND resource_name_is_valid(resource_name_repair(name, 64), false);

UPDATE agents
SET name = resource_name_repair(name, 64)
WHERE name IS DISTINCT FROM resource_name_repair(name, 64)
  AND resource_name_is_valid(resource_name_repair(name, 64), true);

UPDATE machine_pools
SET name = resource_name_repair(name, 64)
WHERE name IS DISTINCT FROM resource_name_repair(name, 64)
  AND resource_name_is_valid(resource_name_repair(name, 64), false);

UPDATE machines
SET display_name = resource_name_repair(display_name, 64)
WHERE display_name IS DISTINCT FROM resource_name_repair(display_name, 64)
  AND resource_name_is_valid(resource_name_repair(display_name, 64), false);

UPDATE machine_daemon_tokens
SET name = resource_name_repair(name, 64)
WHERE name IS DISTINCT FROM resource_name_repair(name, 64)
  AND resource_name_is_valid(resource_name_repair(name, 64), false);

UPDATE cron_triggers
SET name = resource_name_repair(name, 64)
WHERE name IS DISTINCT FROM resource_name_repair(name, 64)
  AND resource_name_is_valid(resource_name_repair(name, 64), false);

DROP FUNCTION resource_name_repair(text, integer);

ALTER TABLE orgs ADD CONSTRAINT orgs_name_policy CHECK (resource_name_is_valid(name, false)) NOT VALID;
ALTER TABLE projects ADD CONSTRAINT projects_name_policy CHECK (resource_name_is_valid(name, false)) NOT VALID;
ALTER TABLE personal_access_tokens ADD CONSTRAINT personal_access_tokens_name_policy CHECK (resource_name_is_valid(name, false)) NOT VALID;
ALTER TABLE auth_device_flows ADD CONSTRAINT auth_device_flows_client_name_policy CHECK (resource_name_is_valid_with_max(client_name, false, 128)) NOT VALID;
ALTER TABLE auth_device_flows ADD CONSTRAINT auth_device_flows_token_name_policy CHECK (resource_name_is_valid(token_name, false)) NOT VALID;
ALTER TABLE org_api_keys ADD CONSTRAINT org_api_keys_name_policy CHECK (resource_name_is_valid(name, false)) NOT VALID;
ALTER TABLE secrets ADD CONSTRAINT secrets_name_policy CHECK (resource_name_is_valid(name, false)) NOT VALID;
ALTER TABLE model_provider_configs ADD CONSTRAINT model_provider_configs_name_policy CHECK (resource_name_is_valid(name, false)) NOT VALID;
ALTER TABLE configured_models ADD CONSTRAINT configured_models_name_policy CHECK (resource_name_is_valid(name, false)) NOT VALID;
ALTER TABLE agent_profiles ADD CONSTRAINT agent_profiles_name_policy CHECK (resource_name_is_valid(name, false)) NOT VALID;
ALTER TABLE agents ADD CONSTRAINT agents_name_policy CHECK (resource_name_is_valid(name, true)) NOT VALID;
ALTER TABLE machine_pools ADD CONSTRAINT machine_pools_name_policy CHECK (resource_name_is_valid(name, false)) NOT VALID;
ALTER TABLE machines ADD CONSTRAINT machines_display_name_policy CHECK (resource_name_is_valid(display_name, false)) NOT VALID;
ALTER TABLE machines ALTER COLUMN display_name DROP DEFAULT;
ALTER TABLE machine_daemon_tokens ADD CONSTRAINT machine_daemon_tokens_name_policy CHECK (resource_name_is_valid(name, false)) NOT VALID;
ALTER TABLE cron_triggers ADD CONSTRAINT cron_triggers_name_policy CHECK (resource_name_is_valid(name, false)) NOT VALID;

-- +goose StatementBegin
CREATE FUNCTION skill_name_is_valid(candidate text) RETURNS boolean AS $$
    SELECT candidate IS NOT NULL
        AND char_length(candidate) BETWEEN 1 AND 64
        AND octet_length(candidate) <= 64
        AND candidate ~ '^[a-z0-9]+(-[a-z0-9]+)*$';
$$ LANGUAGE sql IMMUTABLE;
-- +goose StatementEnd

ALTER TABLE skills ADD CONSTRAINT skills_name_policy CHECK (skill_name_is_valid(name)) NOT VALID;

-- +goose StatementBegin
DO $$
DECLARE
    violation_count bigint;
    detail text;
BEGIN
    WITH violations(table_name, field_name, resource_id) AS (
        SELECT 'orgs', 'name', id::text FROM orgs WHERE NOT resource_name_is_valid(name, false)
        UNION ALL
        SELECT 'projects', 'name', id::text FROM projects WHERE NOT resource_name_is_valid(name, false)
        UNION ALL
        SELECT 'personal_access_tokens', 'name', id::text FROM personal_access_tokens WHERE NOT resource_name_is_valid(name, false)
        UNION ALL
        SELECT 'auth_device_flows', 'client_name', id::text FROM auth_device_flows WHERE NOT resource_name_is_valid_with_max(client_name, false, 128)
        UNION ALL
        SELECT 'auth_device_flows', 'token_name', id::text FROM auth_device_flows WHERE NOT resource_name_is_valid(token_name, false)
        UNION ALL
        SELECT 'org_api_keys', 'name', id::text FROM org_api_keys WHERE NOT resource_name_is_valid(name, false)
        UNION ALL
        SELECT 'secrets', 'name', id::text FROM secrets WHERE NOT resource_name_is_valid(name, false)
        UNION ALL
        SELECT 'model_provider_configs', 'name', id::text FROM model_provider_configs WHERE NOT resource_name_is_valid(name, false)
        UNION ALL
        SELECT 'configured_models', 'name', id::text FROM configured_models WHERE NOT resource_name_is_valid(name, false)
        UNION ALL
        SELECT 'agent_profiles', 'name', id::text FROM agent_profiles WHERE NOT resource_name_is_valid(name, false)
        UNION ALL
        SELECT 'agents', 'name', id::text FROM agents WHERE NOT resource_name_is_valid(name, true)
        UNION ALL
        SELECT 'machine_pools', 'name', id::text FROM machine_pools WHERE NOT resource_name_is_valid(name, false)
        UNION ALL
        SELECT 'machines', 'display_name', id::text FROM machines WHERE NOT resource_name_is_valid(display_name, false)
        UNION ALL
        SELECT 'machine_daemon_tokens', 'name', id::text FROM machine_daemon_tokens WHERE NOT resource_name_is_valid(name, false)
        UNION ALL
        SELECT 'cron_triggers', 'name', id::text FROM cron_triggers WHERE NOT resource_name_is_valid(name, false)
        UNION ALL
        SELECT 'skills', 'name', id::text FROM skills WHERE NOT skill_name_is_valid(name)
    ),
    ranked AS (
        SELECT
            table_name,
            field_name,
            resource_id,
            row_number() OVER (ORDER BY table_name, resource_id, field_name) AS ordinal,
            count(*) OVER () AS total
        FROM violations
    )
    SELECT
        coalesce(max(total), 0),
        string_agg(
            format('%s/%s.%s', table_name, resource_id, field_name),
            ', ' ORDER BY ordinal
        ) FILTER (WHERE ordinal <= 20)
    INTO violation_count, detail
    FROM ranked;

    IF violation_count > 0 THEN
        IF violation_count > 20 THEN
            detail := detail || format(', and %s more', violation_count - 20);
        END IF;
        RAISE EXCEPTION 'resource-name migration blocked; migrate these invalid stored values (%): %',
            violation_count,
            detail
            USING ERRCODE = 'check_violation';
    END IF;
END;
$$;
-- +goose StatementEnd

ALTER TABLE orgs VALIDATE CONSTRAINT orgs_name_policy;
ALTER TABLE projects VALIDATE CONSTRAINT projects_name_policy;
ALTER TABLE personal_access_tokens VALIDATE CONSTRAINT personal_access_tokens_name_policy;
ALTER TABLE auth_device_flows VALIDATE CONSTRAINT auth_device_flows_client_name_policy;
ALTER TABLE auth_device_flows VALIDATE CONSTRAINT auth_device_flows_token_name_policy;
ALTER TABLE org_api_keys VALIDATE CONSTRAINT org_api_keys_name_policy;
ALTER TABLE secrets VALIDATE CONSTRAINT secrets_name_policy;
ALTER TABLE model_provider_configs VALIDATE CONSTRAINT model_provider_configs_name_policy;
ALTER TABLE configured_models VALIDATE CONSTRAINT configured_models_name_policy;
ALTER TABLE agent_profiles VALIDATE CONSTRAINT agent_profiles_name_policy;
ALTER TABLE agents VALIDATE CONSTRAINT agents_name_policy;
ALTER TABLE machine_pools VALIDATE CONSTRAINT machine_pools_name_policy;
ALTER TABLE machines VALIDATE CONSTRAINT machines_display_name_policy;
ALTER TABLE machine_daemon_tokens VALIDATE CONSTRAINT machine_daemon_tokens_name_policy;
ALTER TABLE cron_triggers VALIDATE CONSTRAINT cron_triggers_name_policy;
ALTER TABLE skills VALIDATE CONSTRAINT skills_name_policy;
