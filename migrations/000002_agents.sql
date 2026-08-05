-- +goose Up

-- Model provider configs, agents, reusable profiles, and immutable configs.

CREATE TABLE model_provider_configs (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id uuid NOT NULL REFERENCES orgs(id),
    management_kind text NOT NULL,
    name text NOT NULL,
    api_format text NOT NULL,
    api_variant text NOT NULL DEFAULT 'default',
    base_url text NOT NULL,
    endpoint_path text NOT NULL,
    request_timeout_ms integer NOT NULL DEFAULT 600000,
    auth_kind text NOT NULL,
    auth_options jsonb NOT NULL DEFAULT '{}'::jsonb,
    credential_secret_id uuid,
    deleted_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (org_id, id),
    CHECK (deleted_at IS NOT NULL OR credential_secret_id IS NOT NULL),
    CHECK (management_kind IN ('tenant', 'cluster')),
    CHECK (name <> ''),
    CHECK (api_format IN ('openai-responses', 'openai-chat-completions', 'anthropic-messages')),
    CHECK (api_variant IN ('default', 'openrouter')),
    CHECK (base_url <> '' AND right(base_url, 1) <> '/'),
    CHECK (endpoint_path <> '' AND left(endpoint_path, 1) = '/'),
    CHECK (request_timeout_ms > 0),
    CHECK (auth_kind IN ('bearer_token', 'api_key_header')),
    CHECK (jsonb_typeof(auth_options) = 'object'),
    FOREIGN KEY (org_id, credential_secret_id, management_kind)
        REFERENCES secrets(org_id, id, management_kind)
);

-- +goose StatementBegin
CREATE FUNCTION model_provider_configs_reject_authority_change() RETURNS trigger AS $$
BEGIN
    IF NEW.id IS DISTINCT FROM OLD.id
       OR NEW.org_id IS DISTINCT FROM OLD.org_id
       OR NEW.management_kind IS DISTINCT FROM OLD.management_kind
       OR NEW.api_format IS DISTINCT FROM OLD.api_format
       OR NEW.api_variant IS DISTINCT FROM OLD.api_variant THEN
        RAISE EXCEPTION 'model provider config authority is immutable'
            USING ERRCODE = '25006';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER model_provider_configs_authority_immutable
    BEFORE UPDATE OF id, org_id, management_kind, api_format, api_variant ON model_provider_configs
    FOR EACH ROW
    EXECUTE FUNCTION model_provider_configs_reject_authority_change();

CREATE UNIQUE INDEX model_provider_configs_active_name_idx
    ON model_provider_configs(org_id, name)
    WHERE deleted_at IS NULL;

CREATE TABLE configured_models (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id uuid NOT NULL,
    model_provider_config_id uuid NOT NULL,
    name text NOT NULL,
    current_revision_id uuid NOT NULL,
    deleted_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (org_id, id),
    UNIQUE (org_id, id, model_provider_config_id),
    CHECK (name <> ''),
    FOREIGN KEY (org_id, model_provider_config_id) REFERENCES model_provider_configs(org_id, id)
);

CREATE UNIQUE INDEX configured_models_active_name_idx
    ON configured_models(model_provider_config_id, name)
    WHERE deleted_at IS NULL;

CREATE INDEX configured_models_active_created_idx
    ON configured_models(org_id, model_provider_config_id, created_at DESC, id DESC)
    WHERE deleted_at IS NULL;

CREATE TABLE configured_model_revisions (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id uuid NOT NULL,
    configured_model_id uuid NOT NULL,
    model_provider_config_id uuid NOT NULL,
    provider_model_slug text NOT NULL,
    context_window_tokens integer NOT NULL,
    max_output_tokens integer NOT NULL,
    default_max_output_tokens integer,
    default_cache_retention text,
    supports_tools boolean NOT NULL DEFAULT true,
    supports_reasoning boolean NOT NULL DEFAULT false,
    default_reasoning_effort text NOT NULL DEFAULT '',
    supported_reasoning_efforts text[] NOT NULL DEFAULT '{}'::text[],
    input_modalities text[] NOT NULL DEFAULT '{}'::text[],
    output_modalities text[] NOT NULL DEFAULT '{}'::text[],
    api_variant_options jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL,
    UNIQUE (org_id, id),
    UNIQUE (org_id, configured_model_id, id),
    CHECK (provider_model_slug <> ''),
    CHECK (context_window_tokens > 0),
    CHECK (max_output_tokens > 0),
    CHECK (default_max_output_tokens IS NULL OR default_max_output_tokens > 0),
    CHECK (default_max_output_tokens IS NULL OR default_max_output_tokens <= max_output_tokens),
    CHECK (context_window_tokens > max_output_tokens),
    CHECK (default_cache_retention IS NULL OR default_cache_retention IN ('none', 'short', 'long')),
    CHECK (jsonb_typeof(api_variant_options) = 'object'),
    FOREIGN KEY (org_id, configured_model_id, model_provider_config_id) REFERENCES configured_models(org_id, id, model_provider_config_id)
);

-- configured_models points at its current immutable revision, and each revision
-- points back to its configured model. Add this cyclic FK after both tables exist.
ALTER TABLE configured_models
    ADD FOREIGN KEY (org_id, id, current_revision_id)
    REFERENCES configured_model_revisions(org_id, configured_model_id, id);

-- +goose StatementBegin
CREATE FUNCTION reject_configured_model_revision_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'configured_model_revisions are immutable'
        USING ERRCODE = '25006';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER configured_model_revisions_immutable
    BEFORE UPDATE OR DELETE
    ON configured_model_revisions
    FOR EACH ROW
    EXECUTE FUNCTION reject_configured_model_revision_mutation();

CREATE TABLE project_model_grants (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id uuid NOT NULL,
    project_id uuid NOT NULL,
    configured_model_id uuid NOT NULL,
    context_window_tokens integer,
    max_output_tokens integer,
    default_max_output_tokens integer,
    default_cache_retention text,
    supports_tools boolean,
    supports_reasoning boolean,
    default_reasoning_effort text NOT NULL DEFAULT '',
    supported_reasoning_efforts text[] NOT NULL DEFAULT '{}'::text[],
    input_modalities text[] NOT NULL DEFAULT '{}'::text[],
    output_modalities text[] NOT NULL DEFAULT '{}'::text[],
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK (context_window_tokens IS NULL OR context_window_tokens > 0),
    CHECK (max_output_tokens IS NULL OR max_output_tokens > 0),
    CHECK (default_max_output_tokens IS NULL OR default_max_output_tokens > 0),
    CHECK (default_max_output_tokens IS NULL OR max_output_tokens IS NULL OR default_max_output_tokens <= max_output_tokens),
    CHECK (
        context_window_tokens IS NULL
        OR context_window_tokens > greatest(coalesce(max_output_tokens, 0), coalesce(default_max_output_tokens, 0))
    ),
    CHECK (default_cache_retention IS NULL OR default_cache_retention IN ('none', 'short', 'long')),
    FOREIGN KEY (org_id, project_id) REFERENCES projects(org_id, id),
    FOREIGN KEY (org_id, configured_model_id) REFERENCES configured_models(org_id, id)
);

CREATE UNIQUE INDEX project_model_grants_model_idx
    ON project_model_grants(project_id, configured_model_id);

CREATE INDEX project_model_grants_configured_model_idx
    ON project_model_grants(org_id, configured_model_id);

CREATE TABLE agent_configs (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id uuid NOT NULL REFERENCES orgs(id),
    project_id uuid NOT NULL,
    configured_model_id uuid NOT NULL,
    definition jsonb NOT NULL,
    source text NOT NULL DEFAULT '',
    source_format text NOT NULL DEFAULT 'yaml',
    source_hash text NOT NULL,
    compiled_definition jsonb NOT NULL DEFAULT '{}'::jsonb,
    compiler_version text NOT NULL DEFAULT '',
    effective_definition_hash text NOT NULL,
    created_at timestamptz NOT NULL,
    CHECK (effective_definition_hash <> ''),
    CHECK (source_format IN ('yaml', 'json')),
    CHECK (source_hash <> ''),
    CHECK (jsonb_typeof(definition) = 'object'),
    CHECK (jsonb_typeof(compiled_definition) = 'object'),
    FOREIGN KEY (org_id, project_id) REFERENCES projects(org_id, id),
    FOREIGN KEY (org_id, configured_model_id) REFERENCES configured_models(org_id, id),
    UNIQUE (project_id, id),
    UNIQUE (project_id, effective_definition_hash, source_format, source_hash)
);

-- +goose StatementBegin
CREATE FUNCTION reject_agent_config_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'agent_configs are immutable'
        USING ERRCODE = '25006';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER agent_configs_immutable
    BEFORE UPDATE OR DELETE
    ON agent_configs
    FOR EACH ROW
    EXECUTE FUNCTION reject_agent_config_mutation();

CREATE TABLE agent_profiles (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    project_id uuid NOT NULL,
    name text NOT NULL,
    current_version_id uuid NOT NULL,
    idempotency_key text,
    deleted_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK (name <> ''),
    FOREIGN KEY (project_id) REFERENCES projects(id),
    UNIQUE (project_id, id),
    UNIQUE (project_id, idempotency_key)
);

CREATE UNIQUE INDEX agent_profiles_active_name_idx ON agent_profiles(project_id, name)
    WHERE deleted_at IS NULL;

CREATE INDEX agent_profiles_name_trgm_idx ON agent_profiles USING gin (name gin_trgm_ops)
    WHERE deleted_at IS NULL;

CREATE TABLE agent_profile_versions (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    project_id uuid NOT NULL,
    profile_id uuid NOT NULL,
    generation integer NOT NULL,
    agent_config_id uuid NOT NULL,
    reason text NOT NULL DEFAULT '',
    idempotency_key text,
    created_at timestamptz NOT NULL,
    deleted_at timestamptz,
    CHECK (generation > 0),
    FOREIGN KEY (project_id) REFERENCES projects(id),
    FOREIGN KEY (project_id, profile_id) REFERENCES agent_profiles(project_id, id),
    FOREIGN KEY (project_id, agent_config_id) REFERENCES agent_configs(project_id, id),
    UNIQUE (project_id, profile_id, id),
    UNIQUE (project_id, profile_id, generation),
    UNIQUE (project_id, profile_id, idempotency_key)
);

ALTER TABLE agent_profiles
    ADD FOREIGN KEY (project_id, id, current_version_id)
    REFERENCES agent_profile_versions(project_id, profile_id, id);

-- +goose StatementBegin
CREATE FUNCTION reject_agent_profile_version_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    expected agent_profile_versions;
BEGIN
    IF TG_OP = 'UPDATE'
       AND OLD.deleted_at IS NULL
       AND NEW.deleted_at IS NOT NULL
       AND EXISTS (
           SELECT 1
           FROM agent_profiles profile
           WHERE profile.project_id = OLD.project_id
             AND profile.id = OLD.profile_id
             AND profile.deleted_at IS NOT NULL
       ) THEN
        expected := OLD;
        expected.deleted_at := NEW.deleted_at;
        IF NEW IS NOT DISTINCT FROM expected THEN
            RETURN NEW;
        END IF;
    END IF;

    RAISE EXCEPTION 'agent_profile_versions cannot be mutated directly'
        USING ERRCODE = '25006';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER agent_profile_versions_direct_mutation_blocked
    BEFORE UPDATE OR DELETE
    ON agent_profile_versions
    FOR EACH ROW
    EXECUTE FUNCTION reject_agent_profile_version_mutation();

CREATE TABLE agents (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id uuid NOT NULL REFERENCES orgs(id),
    project_id uuid NOT NULL,
    state text NOT NULL,
    name text NOT NULL DEFAULT '',
    current_config_id uuid NOT NULL,
    integration_target_id uuid,
    idempotency_key text,
    next_event_sequence bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    archived_at timestamptz,
    CHECK (state IN ('active', 'archived')),
    CHECK (next_event_sequence > 0),
    CHECK ((state = 'archived') = (archived_at IS NOT NULL)),
    FOREIGN KEY (org_id, project_id) REFERENCES projects(org_id, id),
    FOREIGN KEY (project_id, current_config_id) REFERENCES agent_configs(project_id, id),
    UNIQUE (project_id, id)
);

CREATE UNIQUE INDEX agents_idempotency_key_idx
    ON agents(project_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

CREATE INDEX agents_active_project_created_idx
    ON agents(project_id, created_at DESC, id DESC)
    WHERE state = 'active';

CREATE INDEX agents_name_trgm_idx ON agents USING gin (name gin_trgm_ops)
    WHERE state = 'active';

-- +goose StatementBegin
CREATE FUNCTION agents_reject_identity_change()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.id IS DISTINCT FROM NEW.id
       OR OLD.org_id IS DISTINCT FROM NEW.org_id
       OR OLD.project_id IS DISTINCT FROM NEW.project_id
       OR OLD.idempotency_key IS DISTINCT FROM NEW.idempotency_key
       OR OLD.created_at IS DISTINCT FROM NEW.created_at THEN
        RAISE EXCEPTION 'agent identity is immutable'
            USING ERRCODE = '25006';
    END IF;

    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER agents_identity_immutable
BEFORE UPDATE OF id, org_id, project_id, idempotency_key, created_at ON agents
FOR EACH ROW EXECUTE FUNCTION agents_reject_identity_change();

CREATE TABLE integration_installs (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id uuid NOT NULL REFERENCES orgs(id),
    project_id uuid NOT NULL,
    agent_profile_id uuid,
    agent_id uuid,
    installed_by_user_id uuid NOT NULL REFERENCES users(id),
    provider text NOT NULL,
    integration_kind text NOT NULL,
    connection_mode text NOT NULL,
    state text NOT NULL,
    provider_tenant_id text NOT NULL,
    provider_account_ref text NOT NULL,
    provider_agent_display_name text NOT NULL DEFAULT '',
    credential_secret_id uuid,
    provider_config jsonb NOT NULL DEFAULT '{}'::jsonb,
    provider_identity jsonb NOT NULL DEFAULT '{}'::jsonb,
    provider_metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    last_oauth_flow_id uuid,
    deleted_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK (provider <> ''),
    CHECK (integration_kind <> ''),
    CHECK (connection_mode <> ''),
    CHECK (state IN ('active', 'disabled')),
    CHECK (provider_tenant_id <> ''),
    CHECK (provider_account_ref <> ''),
    CHECK (jsonb_typeof(provider_config) = 'object'),
    CHECK (jsonb_typeof(provider_identity) = 'object'),
    CHECK (jsonb_typeof(provider_metadata) = 'object'),
    CHECK (last_oauth_flow_id IS NULL OR uuid_extract_version(last_oauth_flow_id) = 7),
    CHECK (
        (agent_profile_id IS NOT NULL AND agent_id IS NULL) OR
        (agent_profile_id IS NULL AND agent_id IS NOT NULL)
    ),
    FOREIGN KEY (org_id, project_id) REFERENCES projects(org_id, id),
    FOREIGN KEY (project_id, agent_profile_id) REFERENCES agent_profiles(project_id, id),
    FOREIGN KEY (project_id, agent_id) REFERENCES agents(project_id, id),
    FOREIGN KEY (org_id, credential_secret_id) REFERENCES secrets(org_id, id),
    UNIQUE (project_id, id)
);

CREATE UNIQUE INDEX integration_installs_provider_tenant_account_idx
    ON integration_installs(provider, provider_tenant_id, provider_account_ref)
    WHERE deleted_at IS NULL;

CREATE UNIQUE INDEX integration_installs_last_oauth_flow_id_idx
    ON integration_installs(last_oauth_flow_id)
    WHERE last_oauth_flow_id IS NOT NULL;

CREATE INDEX integration_installs_credential_secret_idx
    ON integration_installs(org_id, credential_secret_id)
    WHERE credential_secret_id IS NOT NULL;

CREATE TABLE integration_targets (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    project_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    integration_install_id uuid NOT NULL,
    target_ref text NOT NULL,
    provider_ref text NOT NULL,
    provider_ref_kind text NOT NULL,
    display_name text NOT NULL DEFAULT '',
    provider_metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    deleted_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK (target_ref <> ''),
    CHECK (provider_ref <> ''),
    CHECK (provider_ref_kind <> ''),
    CHECK (jsonb_typeof(provider_metadata) = 'object'),
    FOREIGN KEY (project_id) REFERENCES projects(id),
    FOREIGN KEY (project_id, agent_id) REFERENCES agents(project_id, id),
    FOREIGN KEY (project_id, integration_install_id) REFERENCES integration_installs(project_id, id),
    UNIQUE (project_id, agent_id, id)
);

-- integrations_store.go relies on this name to detect target-ref collisions.
CREATE UNIQUE INDEX integration_targets_agent_target_ref_idx
    ON integration_targets(project_id, agent_id, target_ref);

CREATE UNIQUE INDEX integration_targets_active_provider_ref_idx
    ON integration_targets(project_id, integration_install_id, provider_ref)
    WHERE deleted_at IS NULL;

ALTER TABLE agents
    ADD FOREIGN KEY (project_id, id, integration_target_id)
    REFERENCES integration_targets(project_id, agent_id, id);
