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
) RETURNS boolean AS $$
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
$$ LANGUAGE sql IMMUTABLE;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION resource_name_is_valid(candidate text, allow_empty boolean) RETURNS boolean AS $$
    SELECT resource_name_is_valid_with_max(candidate, allow_empty, 64);
$$ LANGUAGE sql IMMUTABLE;
-- +goose StatementEnd

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
ALTER TABLE agent_configs ADD CONSTRAINT agent_configs_source_required CHECK (source <> '') NOT VALID;
ALTER TABLE agent_configs ALTER COLUMN source DROP DEFAULT;

-- +goose StatementBegin
CREATE FUNCTION skill_name_is_valid(candidate text) RETURNS boolean AS $$
    SELECT candidate IS NOT NULL
        AND char_length(candidate) BETWEEN 1 AND 64
        AND octet_length(candidate) <= 64
        AND candidate ~ '^[a-z0-9]+(-[a-z0-9]+)*$';
$$ LANGUAGE sql IMMUTABLE;
-- +goose StatementEnd

ALTER TABLE skills ADD CONSTRAINT skills_name_policy CHECK (skill_name_is_valid(name)) NOT VALID;
