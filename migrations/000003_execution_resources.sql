-- +goose Up

-- Machine pools, machines, provider observations, and checkpoints.

CREATE TABLE machine_pools (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id uuid NOT NULL REFERENCES orgs(id),
    name text NOT NULL,
    management_kind text NOT NULL,
    description text NOT NULL DEFAULT '',
    provider text NOT NULL,
    default_machine_cpu integer,
    default_machine_memory_mb integer,
    default_machine_env jsonb NOT NULL DEFAULT '{}'::jsonb,
    default_machine_secret_env jsonb NOT NULL DEFAULT '{}'::jsonb,
    default_machine_provider_options jsonb NOT NULL DEFAULT '{}'::jsonb,
    default_cwd text NOT NULL DEFAULT '',
    provider_config jsonb NOT NULL DEFAULT '{}'::jsonb,
    provider_auth_secret_id uuid,
    deletion_provider_auth_secret_version_id uuid,
    provider_auth_env_var text NOT NULL DEFAULT '',
    max_total_machines integer NOT NULL,
    max_total_cpu integer,
    max_total_memory_mb integer,
    max_machine_cpu integer,
    max_machine_memory_mb integer,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    deleted_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK (name <> ''),
    CHECK (management_kind IN ('tenant', 'cluster')),
    CHECK (provider <> ''),
    CHECK (default_machine_cpu IS NULL OR default_machine_cpu > 0),
    CHECK (default_machine_memory_mb IS NULL OR default_machine_memory_mb > 0),
    CHECK (jsonb_typeof(default_machine_env) = 'object'),
    CHECK (jsonb_typeof(default_machine_secret_env) = 'object'),
    CHECK (jsonb_typeof(default_machine_provider_options) = 'object'),
    CHECK (jsonb_typeof(provider_config) = 'object'),
    -- Tenant pools hold their provider credential while live; deletion releases
    -- it (once machine teardown no longer needs it) so the secret can be
    -- deleted.
    CHECK (
        (management_kind = 'tenant' AND (provider_auth_secret_id IS NOT NULL OR deleted_at IS NOT NULL) AND provider_auth_env_var = '') OR
        (management_kind = 'cluster' AND provider_auth_secret_id IS NULL AND provider_auth_env_var <> '')
    ),
    CHECK (
        deletion_provider_auth_secret_version_id IS NULL OR
        (management_kind = 'tenant' AND provider_auth_secret_id IS NOT NULL AND deleted_at IS NOT NULL)
    ),
    CHECK (
        management_kind <> 'tenant' OR deleted_at IS NULL OR provider_auth_secret_id IS NULL OR
        deletion_provider_auth_secret_version_id IS NOT NULL
    ),
    CHECK (max_total_machines >= 0),
    CHECK (max_total_cpu IS NULL OR max_total_cpu >= 0),
    CHECK (max_total_memory_mb IS NULL OR max_total_memory_mb >= 0),
    CHECK (max_machine_cpu IS NULL OR max_machine_cpu > 0),
    CHECK (max_machine_memory_mb IS NULL OR max_machine_memory_mb > 0),
    CHECK (default_machine_cpu IS NULL OR max_machine_cpu IS NULL OR default_machine_cpu <= max_machine_cpu),
    CHECK (default_machine_memory_mb IS NULL OR max_machine_memory_mb IS NULL OR default_machine_memory_mb <= max_machine_memory_mb),
    CHECK (jsonb_typeof(metadata) = 'object'),
    FOREIGN KEY (org_id, provider_auth_secret_id, management_kind) REFERENCES secrets(org_id, id, management_kind),
    FOREIGN KEY (provider_auth_secret_id, deletion_provider_auth_secret_version_id)
        REFERENCES secret_versions(secret_id, id),
    UNIQUE (org_id, id)
);

CREATE UNIQUE INDEX machine_pools_active_name_unique_idx
    ON machine_pools(org_id, name)
    WHERE deleted_at IS NULL;

-- +goose StatementBegin
CREATE FUNCTION machine_pools_reject_authority_change() RETURNS trigger AS $$
BEGIN
    IF NEW.id IS DISTINCT FROM OLD.id
       OR NEW.org_id IS DISTINCT FROM OLD.org_id
       OR NEW.management_kind IS DISTINCT FROM OLD.management_kind
       OR NEW.provider IS DISTINCT FROM OLD.provider
       OR NEW.provider_auth_env_var IS DISTINCT FROM OLD.provider_auth_env_var THEN
        RAISE EXCEPTION 'machine pool authority is immutable'
            USING ERRCODE = '25006';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER machine_pools_authority_immutable
    BEFORE UPDATE OF id, org_id, management_kind, provider, provider_auth_env_var ON machine_pools
    FOR EACH ROW
    EXECUTE FUNCTION machine_pools_reject_authority_change();

CREATE TABLE machines (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id uuid NOT NULL REFERENCES orgs(id),
    machine_pool_id uuid,
    current_daemon_runtime_id uuid,
    source_kind text NOT NULL,
    display_name text NOT NULL DEFAULT '',
    description text NOT NULL DEFAULT '',
    provider text NOT NULL,
    lifecycle_state text NOT NULL,
    lifecycle_changed_at timestamptz NOT NULL,
    lifecycle_version bigint NOT NULL DEFAULT 1,
    provider_resource_id text,
    provider_provision_attempted_at timestamptz,
    sandbox_url text,
    asleep_since timestamptz,
    last_observed_at timestamptz,
    cpu integer,
    memory_mb integer,
    cwd text NOT NULL DEFAULT '',
    env jsonb NOT NULL DEFAULT '{}'::jsonb,
    secret_env jsonb NOT NULL DEFAULT '{}'::jsonb,
    provider_options jsonb,
    idempotency_key text,
    lifecycle_reason_code text,
    lifecycle_reason_message text NOT NULL DEFAULT '',
    next_reconcile_after timestamptz,
    provision_attempts integer NOT NULL DEFAULT 0,
    delete_attempts integer NOT NULL DEFAULT 0,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    deleted_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK (source_kind IN ('byo', 'pool')),
    CHECK ((source_kind = 'pool') = (machine_pool_id IS NOT NULL)),
    CHECK (
        source_kind = 'pool' OR provider_provision_attempted_at IS NULL
    ),
    CHECK (
        provider_resource_id IS NULL OR btrim(provider_resource_id) <> ''
    ),
    CHECK (
        source_kind <> 'pool'
        OR lifecycle_state <> 'active'
        OR provider_resource_id IS NOT NULL
    ),
    CHECK (lifecycle_state IN ('provisioning', 'provision_failed', 'active', 'deleting', 'delete_failed', 'deleted')),
    CHECK (source_kind = 'pool' OR lifecycle_state IN ('active', 'deleted')),
    CHECK (
        source_kind <> 'pool'
        OR (lifecycle_state IN ('active', 'deleted') AND next_reconcile_after IS NULL)
        OR (lifecycle_state NOT IN ('active', 'deleted') AND next_reconcile_after IS NOT NULL)
    ),
    CHECK ((lifecycle_state = 'deleted') = (deleted_at IS NOT NULL)),
    CHECK (provision_attempts >= 0),
    CHECK (delete_attempts >= 0),
    CHECK (lifecycle_version > 0),
    CHECK (
        (
            source_kind = 'pool'
            AND (cpu IS NULL OR cpu > 0)
            AND (memory_mb IS NULL OR memory_mb > 0)
            AND provider_options IS NOT NULL AND jsonb_typeof(provider_options) = 'object'
        )
        OR (
            source_kind = 'byo'
            AND cpu IS NULL
            AND memory_mb IS NULL
            AND provider_options IS NULL
        )
    ),
    CHECK (jsonb_typeof(env) = 'object'),
    CHECK (jsonb_typeof(secret_env) = 'object'),
    CHECK (jsonb_typeof(metadata) = 'object'),
    FOREIGN KEY (org_id, machine_pool_id) REFERENCES machine_pools(org_id, id),
    UNIQUE (org_id, id),
    UNIQUE (org_id, idempotency_key)
);

CREATE UNIQUE INDEX machines_provider_resource_unique_idx
    ON machines(org_id, provider, provider_resource_id)
    WHERE provider_resource_id IS NOT NULL;

CREATE UNIQUE INDEX machines_byo_active_display_name_unique_idx
    ON machines(org_id, display_name)
    WHERE source_kind = 'byo' AND deleted_at IS NULL;

CREATE INDEX machines_pool_lifecycle_reconcile_idx
    ON machines(lifecycle_state, next_reconcile_after, updated_at, created_at, id)
    WHERE source_kind = 'pool' AND deleted_at IS NULL;

CREATE INDEX machines_active_created_idx
    ON machines(org_id, created_at DESC, id DESC)
    WHERE lifecycle_state = 'active' AND deleted_at IS NULL;

CREATE INDEX machines_live_org_idx
    ON machines(org_id, id)
    WHERE deleted_at IS NULL;

CREATE INDEX machines_live_pool_idx
    ON machines(org_id, machine_pool_id, id)
    WHERE source_kind = 'pool' AND deleted_at IS NULL;

CREATE INDEX machines_display_name_trgm_idx ON machines USING gin (display_name gin_trgm_ops);

-- +goose StatementBegin
CREATE FUNCTION machines_reject_authority_change() RETURNS trigger AS $$
BEGIN
    IF OLD.id IS DISTINCT FROM NEW.id
       OR OLD.org_id IS DISTINCT FROM NEW.org_id
       OR OLD.source_kind <> NEW.source_kind
       OR OLD.machine_pool_id IS DISTINCT FROM NEW.machine_pool_id
       OR OLD.provider <> NEW.provider
       OR OLD.provider_options IS DISTINCT FROM NEW.provider_options
       OR (
           (OLD.cpu IS DISTINCT FROM NEW.cpu OR OLD.memory_mb IS DISTINCT FROM NEW.memory_mb)
           AND NOT (
               OLD.source_kind = 'pool'
               AND OLD.lifecycle_state = 'provisioning'
               AND NEW.lifecycle_state = 'provisioning'
               AND (OLD.cpu IS NULL OR OLD.cpu IS NOT DISTINCT FROM NEW.cpu)
               AND (OLD.memory_mb IS NULL OR OLD.memory_mb IS NOT DISTINCT FROM NEW.memory_mb)
               AND (NEW.cpu IS NULL OR NEW.cpu > 0)
               AND (NEW.memory_mb IS NULL OR NEW.memory_mb > 0)
           )
       ) THEN
        RAISE EXCEPTION 'machine authority and provisioning columns are immutable'
            USING ERRCODE = '25006';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER machines_authority_immutable
    BEFORE UPDATE OF id, org_id, source_kind, machine_pool_id, provider, provider_options, cpu, memory_mb ON machines
    FOR EACH ROW
    EXECUTE FUNCTION machines_reject_authority_change();

CREATE TABLE project_machine_pool_grants (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id uuid NOT NULL,
    project_id uuid NOT NULL,
    machine_pool_id uuid NOT NULL,
    description text NOT NULL DEFAULT '',
    default_machine_cpu integer,
    default_machine_memory_mb integer,
    default_machine_env_overlay jsonb NOT NULL DEFAULT '{}'::jsonb,
    default_machine_secret_env_overlay jsonb NOT NULL DEFAULT '{}'::jsonb,
    default_machine_provider_options_overlay jsonb NOT NULL DEFAULT '{}'::jsonb,
    default_cwd text NOT NULL DEFAULT '',
    max_total_machines integer,
    max_total_cpu integer,
    max_total_memory_mb integer,
    max_machine_cpu integer,
    max_machine_memory_mb integer,
    idempotency_key text,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (project_id, idempotency_key),
    UNIQUE (project_id, id),
    CHECK (default_machine_cpu IS NULL OR default_machine_cpu > 0),
    CHECK (default_machine_memory_mb IS NULL OR default_machine_memory_mb > 0),
    CHECK (jsonb_typeof(default_machine_env_overlay) = 'object'),
    CHECK (jsonb_typeof(default_machine_secret_env_overlay) = 'object'),
    CHECK (jsonb_typeof(default_machine_provider_options_overlay) = 'object'),
    CHECK (max_total_machines IS NULL OR max_total_machines >= 0),
    CHECK (max_total_cpu IS NULL OR max_total_cpu >= 0),
    CHECK (max_total_memory_mb IS NULL OR max_total_memory_mb >= 0),
    CHECK (max_machine_cpu IS NULL OR max_machine_cpu > 0),
    CHECK (max_machine_memory_mb IS NULL OR max_machine_memory_mb > 0),
    CHECK (default_machine_cpu IS NULL OR max_machine_cpu IS NULL OR default_machine_cpu <= max_machine_cpu),
    CHECK (default_machine_memory_mb IS NULL OR max_machine_memory_mb IS NULL OR default_machine_memory_mb <= max_machine_memory_mb),
    CHECK (jsonb_typeof(metadata) = 'object'),
    FOREIGN KEY (org_id, project_id) REFERENCES projects(org_id, id),
    FOREIGN KEY (org_id, machine_pool_id) REFERENCES machine_pools(org_id, id)
);

CREATE UNIQUE INDEX project_machine_pool_grants_pool_idx
    ON project_machine_pool_grants(project_id, machine_pool_id);

CREATE INDEX project_machine_pool_grants_machine_pool_idx
    ON project_machine_pool_grants(org_id, machine_pool_id, id);

CREATE TABLE project_machine_grants (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id uuid NOT NULL,
    project_id uuid NOT NULL,
    machine_id uuid NOT NULL,
    source_kind text NOT NULL DEFAULT 'explicit',
    project_machine_pool_grant_id uuid,
    description text NOT NULL DEFAULT '',
    idempotency_key text,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (project_id, idempotency_key),
    UNIQUE (project_id, machine_id),
    CHECK (source_kind IN ('explicit', 'pool')),
    CHECK ((source_kind = 'pool') = (project_machine_pool_grant_id IS NOT NULL)),
    CHECK (jsonb_typeof(metadata) = 'object'),
    FOREIGN KEY (org_id, project_id) REFERENCES projects(org_id, id),
    FOREIGN KEY (org_id, machine_id) REFERENCES machines(org_id, id),
    FOREIGN KEY (project_id, project_machine_pool_grant_id) REFERENCES project_machine_pool_grants(project_id, id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX project_machine_grants_one_pool_machine_idx
    ON project_machine_grants(org_id, machine_id)
    WHERE source_kind = 'pool';

CREATE INDEX project_machine_grants_machine_idx
    ON project_machine_grants(org_id, machine_id, project_id, id);

CREATE INDEX project_machine_grants_project_explicit_created_idx
    ON project_machine_grants(org_id, project_id, created_at DESC, id DESC)
    WHERE source_kind = 'explicit';

CREATE INDEX project_machine_grants_pool_grant_idx
    ON project_machine_grants(project_id, project_machine_pool_grant_id, org_id)
    WHERE project_machine_pool_grant_id IS NOT NULL;

CREATE TABLE agent_machine_bindings (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id uuid NOT NULL REFERENCES orgs(id),
    project_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    create_tool_call_id uuid,
    delete_tool_call_id uuid,
    machine_id uuid NOT NULL,
    machine_ref text NOT NULL,
    binding_kind text NOT NULL,
    state text NOT NULL DEFAULT 'attached',
    description text NOT NULL DEFAULT '',
    cwd text NOT NULL DEFAULT '',
    env_overlay jsonb NOT NULL DEFAULT '{}'::jsonb,
    secret_env_overlay jsonb NOT NULL DEFAULT '{}'::jsonb,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK (binding_kind IN ('explicit', 'pool')),
    CHECK (machine_ref ~ '^mchr-[a-z0-9]{6}$'),
    CHECK (state IN ('attached', 'released')),
    CHECK (jsonb_typeof(env_overlay) = 'object'),
    CHECK (jsonb_typeof(secret_env_overlay) = 'object'),
    CHECK (jsonb_typeof(metadata) = 'object'),
    FOREIGN KEY (org_id, project_id) REFERENCES projects(org_id, id),
    FOREIGN KEY (project_id, agent_id) REFERENCES agents(project_id, id),
    FOREIGN KEY (org_id, machine_id) REFERENCES machines(org_id, id),
    UNIQUE (project_id, agent_id, id, machine_id),
    UNIQUE (project_id, agent_id, machine_ref)
);

CREATE UNIQUE INDEX agent_machine_bindings_attached_machine_unique_idx
    ON agent_machine_bindings(project_id, agent_id, machine_id)
    WHERE state = 'attached';

CREATE INDEX agent_machine_bindings_live_machine_idx
    ON agent_machine_bindings(org_id, machine_id)
    WHERE state = 'attached';

CREATE UNIQUE INDEX agent_machine_bindings_create_tool_call_idx
    ON agent_machine_bindings(project_id, agent_id, create_tool_call_id)
    WHERE create_tool_call_id IS NOT NULL;

CREATE UNIQUE INDEX agent_machine_bindings_delete_tool_call_idx
    ON agent_machine_bindings(project_id, agent_id, delete_tool_call_id)
    WHERE delete_tool_call_id IS NOT NULL;
