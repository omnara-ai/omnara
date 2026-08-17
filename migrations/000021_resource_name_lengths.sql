-- +goose Up

-- Go additionally rejects Unicode format characters that PostgreSQL cannot classify portably.
-- +goose StatementBegin
CREATE FUNCTION resource_name_is_valid(candidate text, allow_empty boolean) RETURNS boolean AS $$
    SELECT candidate IS NOT NULL
        AND (allow_empty OR candidate <> '')
        AND char_length(candidate) <= 64
        AND octet_length(candidate) <= 256
        AND candidate !~ '[[:cntrl:]]'
        AND candidate = btrim(candidate, ' ')
        AND replace(candidate, ' ', '') !~ '[[:space:]]';
$$ LANGUAGE sql IMMUTABLE;
-- +goose StatementEnd

ALTER TABLE orgs ADD CONSTRAINT orgs_name_policy CHECK (resource_name_is_valid(name, false));
ALTER TABLE projects ADD CONSTRAINT projects_name_policy CHECK (resource_name_is_valid(name, false));
ALTER TABLE personal_access_tokens ADD CONSTRAINT personal_access_tokens_name_policy CHECK (resource_name_is_valid(name, false));
ALTER TABLE auth_device_flows ADD CONSTRAINT auth_device_flows_token_name_policy CHECK (resource_name_is_valid(token_name, false));
ALTER TABLE org_api_keys ADD CONSTRAINT org_api_keys_name_policy CHECK (resource_name_is_valid(name, false));
ALTER TABLE secrets ADD CONSTRAINT secrets_name_policy CHECK (resource_name_is_valid(name, false));
ALTER TABLE model_provider_configs ADD CONSTRAINT model_provider_configs_name_policy CHECK (resource_name_is_valid(name, false));
ALTER TABLE configured_models ADD CONSTRAINT configured_models_name_policy CHECK (resource_name_is_valid(name, false));
ALTER TABLE agent_profiles ADD CONSTRAINT agent_profiles_name_policy CHECK (resource_name_is_valid(name, false));
ALTER TABLE agents ADD CONSTRAINT agents_name_policy CHECK (resource_name_is_valid(name, true));
ALTER TABLE machine_pools ADD CONSTRAINT machine_pools_name_policy CHECK (resource_name_is_valid(name, false));
ALTER TABLE machines ADD CONSTRAINT machines_display_name_policy CHECK (resource_name_is_valid(display_name, false));
ALTER TABLE machine_daemon_tokens ADD CONSTRAINT machine_daemon_tokens_name_policy CHECK (resource_name_is_valid(name, false));
ALTER TABLE cron_triggers ADD CONSTRAINT cron_triggers_name_policy CHECK (resource_name_is_valid(name, false));

-- +goose StatementBegin
CREATE FUNCTION skill_name_is_valid(candidate text) RETURNS boolean AS $$
    SELECT candidate IS NOT NULL
        AND char_length(candidate) BETWEEN 1 AND 64
        AND octet_length(candidate) <= 64
        AND candidate ~ '^[a-z0-9]+(-[a-z0-9]+)*$';
$$ LANGUAGE sql IMMUTABLE;
-- +goose StatementEnd

ALTER TABLE skills ADD CONSTRAINT skills_name_policy CHECK (skill_name_is_valid(name));
