-- +goose Up

-- Triggers grandfather unchanged legacy names while enforcing inserts and renames.
-- Go remains authoritative for Unicode categories PostgreSQL cannot mirror portably.
-- +goose StatementBegin
CREATE FUNCTION enforce_resource_name_write() RETURNS trigger AS $$
DECLARE
    field_name text := TG_ARGV[0];
    allow_empty boolean := TG_ARGV[1]::boolean;
    value text;
    old_value text;
BEGIN
    value := to_jsonb(NEW) ->> field_name;
    IF TG_OP = 'UPDATE' THEN
        old_value := to_jsonb(OLD) ->> field_name;
        IF value IS NOT DISTINCT FROM old_value THEN
            RETURN NEW;
        END IF;
    END IF;

    IF value IS NULL
        OR (NOT allow_empty AND value = '')
        OR char_length(value) > 64
        OR octet_length(value) > 256
        OR value ~ '[[:cntrl:]]'
        OR value <> btrim(value, ' ')
        OR replace(value, ' ', '') ~ '[[:space:]]'
    THEN
        RAISE EXCEPTION '% must be a valid resource name', field_name
            USING ERRCODE = '23514', CONSTRAINT = TG_NAME;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER orgs_name_policy
    BEFORE INSERT OR UPDATE OF name ON orgs
    FOR EACH ROW EXECUTE FUNCTION enforce_resource_name_write('name', 'false');
CREATE TRIGGER projects_name_policy
    BEFORE INSERT OR UPDATE OF name ON projects
    FOR EACH ROW EXECUTE FUNCTION enforce_resource_name_write('name', 'false');
CREATE TRIGGER personal_access_tokens_name_policy
    BEFORE INSERT OR UPDATE OF name ON personal_access_tokens
    FOR EACH ROW EXECUTE FUNCTION enforce_resource_name_write('name', 'false');
CREATE TRIGGER auth_device_flows_token_name_policy
    BEFORE INSERT OR UPDATE OF token_name ON auth_device_flows
    FOR EACH ROW EXECUTE FUNCTION enforce_resource_name_write('token_name', 'false');
CREATE TRIGGER org_api_keys_name_policy
    BEFORE INSERT OR UPDATE OF name ON org_api_keys
    FOR EACH ROW EXECUTE FUNCTION enforce_resource_name_write('name', 'false');
CREATE TRIGGER secrets_name_policy
    BEFORE INSERT OR UPDATE OF name ON secrets
    FOR EACH ROW EXECUTE FUNCTION enforce_resource_name_write('name', 'false');
CREATE TRIGGER model_provider_configs_name_policy
    BEFORE INSERT OR UPDATE OF name ON model_provider_configs
    FOR EACH ROW EXECUTE FUNCTION enforce_resource_name_write('name', 'false');
CREATE TRIGGER configured_models_name_policy
    BEFORE INSERT OR UPDATE OF name ON configured_models
    FOR EACH ROW EXECUTE FUNCTION enforce_resource_name_write('name', 'false');
CREATE TRIGGER agent_profiles_name_policy
    BEFORE INSERT OR UPDATE OF name ON agent_profiles
    FOR EACH ROW EXECUTE FUNCTION enforce_resource_name_write('name', 'false');
CREATE TRIGGER agents_name_policy
    BEFORE INSERT OR UPDATE OF name ON agents
    FOR EACH ROW EXECUTE FUNCTION enforce_resource_name_write('name', 'true');
CREATE TRIGGER machine_pools_name_policy
    BEFORE INSERT OR UPDATE OF name ON machine_pools
    FOR EACH ROW EXECUTE FUNCTION enforce_resource_name_write('name', 'false');
CREATE TRIGGER machines_display_name_policy
    BEFORE INSERT OR UPDATE OF display_name ON machines
    FOR EACH ROW EXECUTE FUNCTION enforce_resource_name_write('display_name', 'false');
CREATE TRIGGER machine_daemon_tokens_name_policy
    BEFORE INSERT OR UPDATE OF name ON machine_daemon_tokens
    FOR EACH ROW EXECUTE FUNCTION enforce_resource_name_write('name', 'false');

-- +goose StatementBegin
CREATE FUNCTION enforce_skill_name_write() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND NEW.name IS NOT DISTINCT FROM OLD.name THEN
        RETURN NEW;
    END IF;
    IF NEW.name IS NULL
        OR char_length(NEW.name) NOT BETWEEN 1 AND 64
        OR octet_length(NEW.name) > 64
        OR NEW.name !~ '^[a-z0-9]+(-[a-z0-9]+)*$'
    THEN
        RAISE EXCEPTION 'name must be a valid skill identifier'
            USING ERRCODE = '23514', CONSTRAINT = TG_NAME;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER skills_name_policy
    BEFORE INSERT OR UPDATE OF name ON skills
    FOR EACH ROW EXECUTE FUNCTION enforce_skill_name_write();
