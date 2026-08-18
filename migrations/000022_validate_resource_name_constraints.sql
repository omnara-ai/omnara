-- +goose Up

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
ALTER TABLE agent_configs VALIDATE CONSTRAINT agent_configs_source_required;
ALTER TABLE skills VALIDATE CONSTRAINT skills_name_policy;
