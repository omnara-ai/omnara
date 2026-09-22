-- +goose Up

-- Goose commits each migration separately: reject unfinished work before SQL45
-- changes the schema needed by the old release to stop it. Go46 rechecks under lock.
-- Keep this legacy-policy preflight in sync with Go46's frozen translator.
-- +goose StatementBegin
DO $$
DECLARE conflicting_config uuid; conflicting_tool text; unfinished_agent uuid;
        unsupported_install uuid; ambiguous_agent uuid; ambiguous_targets uuid[];
        policy_config uuid; policy_project uuid; invalid_target uuid; full_project uuid;
BEGIN
    IF EXISTS (SELECT 1 FROM agent_runtime_locks WHERE lease_expires_at > statement_timestamp())
       OR EXISTS (SELECT 1 FROM model_call_contexts WHERE state = 'started')
       OR EXISTS (SELECT 1 FROM tool_calls WHERE state <> 'completed')
       OR EXISTS (SELECT 1 FROM agent_interactions WHERE state = 'open') THEN
        RAISE EXCEPTION 'app cutover requires maintenance: stop outstanding work and interactions through the old release, then stop writers';
    END IF;
    SELECT agent.id INTO unfinished_agent FROM agents agent
    JOIN LATERAL (SELECT id FROM agent_turns WHERE agent_id=agent.id ORDER BY turn_sequence DESC LIMIT 1) latest ON true
    WHERE EXISTS (SELECT 1 FROM agent_continuable_model_contexts(agent.project_id,agent.id) context WHERE context.turn_id=latest.id)
       OR agent_has_incomplete_tool_batch(agent.project_id,agent.id)
       OR EXISTS (SELECT 1 FROM agent_next_model_work(agent.project_id,agent.id) frontier WHERE frontier.turn_id=latest.id)
    LIMIT 1;
    IF unfinished_agent IS NOT NULL THEN
        RAISE EXCEPTION 'app cutover: agent % still has continuable work; stop it through the old release', unfinished_agent;
    END IF;
    SELECT config.id, tool.key INTO conflicting_config, conflicting_tool
    FROM agent_configs config CROSS JOIN LATERAL jsonb_each(coalesce(nullif(config.compiled_definition->'tools','null'::jsonb),'{}'::jsonb)) tool
    WHERE tool.value->>'type' = 'custom'
      AND (starts_with(tool.key, 'app__') OR tool.key IN ('list_interaction_handlers','set_interaction_handler'))
    LIMIT 1;
    IF conflicting_config IS NOT NULL THEN
        RAISE EXCEPTION 'config % custom tool % conflicts with a new app built-in; historical configs require repair before cutover', conflicting_config, conflicting_tool;
    END IF;
    SELECT id INTO unsupported_install FROM integration_installs
    WHERE provider <> 'slack' OR agent_id IS NOT NULL OR agent_profile_id IS NULL
       OR connection_mode <> 'webhook'
       OR (state='active' AND deleted_at IS NULL AND credential_secret_id IS NULL) LIMIT 1;
    IF unsupported_install IS NOT NULL THEN
        RAISE EXCEPTION 'install % is not a profile-bound Slack webhook setup; review and repair before app cutover', unsupported_install;
    END IF;
    SELECT config.id, config.project_id INTO policy_config, policy_project
    FROM agent_configs config
    CROSS JOIN LATERAL (SELECT config.compiled_definition->'tools'->'send_integration_message' AS value) policy
    WHERE policy.value IS NOT NULL AND (
        jsonb_typeof(policy.value) <> 'object'
        OR policy.value - ARRAY['enabled','deferred','permission']::text[] <> '{}'::jsonb
        OR (policy.value ? 'enabled' AND jsonb_typeof(policy.value->'enabled') <> 'boolean')
        OR (policy.value ? 'deferred' AND jsonb_typeof(policy.value->'deferred') <> 'boolean')
        OR (policy.value ? 'permission' AND (
            jsonb_typeof(policy.value->'permission') <> 'object'
            OR (policy.value->'permission'->>'mode') IS DISTINCT FROM 'always_allow'
            OR (policy.value->'permission') - ARRAY['mode','parameters']::text[] <> '{}'::jsonb
            OR (policy.value->'permission' ? 'parameters'
                AND policy.value->'permission'->'parameters' <> '{}'::jsonb)))
        OR (policy.value->'enabled' = 'false'::jsonb AND NOT EXISTS (
            SELECT 1 FROM integration_installs install WHERE install.project_id = config.project_id)))
    LIMIT 1;
    IF policy_config IS NOT NULL THEN
        RAISE EXCEPTION 'project % config % has an unmappable legacy send policy; repair before app cutover', policy_project, policy_config;
    END IF;
    SELECT target.agent_id, array_agg(target.id ORDER BY target.id)
    INTO ambiguous_agent, ambiguous_targets
    FROM integration_targets target
    JOIN integration_installs install ON install.id = target.integration_install_id
    JOIN agents agent ON agent.project_id = target.project_id AND agent.id = target.agent_id
    JOIN projects project ON project.id = target.project_id
    JOIN orgs org ON org.id = project.org_id
    WHERE target.deleted_at IS NULL AND install.deleted_at IS NULL
      AND project.deleted_at IS NULL AND org.deleted_at IS NULL
    GROUP BY target.agent_id, target.integration_install_id HAVING count(*) > 1 LIMIT 1;
    IF ambiguous_agent IS NOT NULL THEN
        RAISE EXCEPTION 'agent % has several live targets through one Slack app: %. Review these destinations before cutover; no target has been removed or broadened', ambiguous_agent, ambiguous_targets;
    END IF;
    SELECT target.id INTO invalid_target
    FROM integration_targets target
    JOIN integration_installs install ON install.id=target.integration_install_id AND install.deleted_at IS NULL
    JOIN agents agent ON agent.project_id=target.project_id AND agent.id=target.agent_id
    JOIN projects project ON project.id=target.project_id AND project.deleted_at IS NULL
    JOIN orgs org ON org.id=project.org_id AND org.deleted_at IS NULL
    WHERE target.deleted_at IS NULL AND NOT CASE target.provider_ref_kind
        WHEN 'dm' THEN target.provider_ref COLLATE "C" ~ '^[CDG][A-Z0-9]+$'
        WHEN 'channel' THEN target.provider_ref COLLATE "C" ~ '^[CDG][A-Z0-9]+$'
        WHEN 'thread' THEN target.provider_ref COLLATE "C" ~ '^[CDG][A-Z0-9]+:[0-9]+[.][0-9]+$'
        ELSE false END
    LIMIT 1;
    IF invalid_target IS NOT NULL THEN
        RAISE EXCEPTION 'invalid Slack target %; repair before app cutover', invalid_target;
    END IF;
    WITH successors AS (
        SELECT agent.project_id, count(DISTINCT agent.id) AS needed
        FROM agents agent
        JOIN projects project ON project.id=agent.project_id AND project.deleted_at IS NULL
        JOIN orgs org ON org.id=project.org_id AND org.deleted_at IS NULL
        JOIN integration_targets target ON target.project_id=agent.project_id AND target.agent_id=agent.id
            AND target.deleted_at IS NULL
        JOIN integration_installs install ON install.id=target.integration_install_id AND install.deleted_at IS NULL
        GROUP BY agent.project_id
    ), config_counts AS (
        SELECT project_id, count(*) AS existing FROM agent_configs GROUP BY project_id
    )
    SELECT project.id INTO full_project FROM successors
    JOIN projects project ON project.id=successors.project_id
    JOIN config_counts configs ON configs.project_id=project.id
    LEFT JOIN org_resource_limit_overrides limits ON limits.org_id=project.org_id
    WHERE configs.existing + successors.needed > coalesce(limits.max_agent_configs_per_project, 10000000)
    LIMIT 1;
    IF full_project IS NOT NULL THEN
        RAISE EXCEPTION 'project % needs additional config quota for Slack successors; raise the existing org override before app cutover', full_project;
    END IF;
END;
$$;
-- +goose StatementEnd

ALTER TABLE integration_installs RENAME TO project_apps;
ALTER TABLE integration_targets RENAME COLUMN integration_install_id TO app_id;
DROP INDEX integration_installs_provider_tenant_account_idx;
ALTER INDEX integration_installs_last_oauth_flow_id_idx RENAME TO project_apps_last_oauth_flow_id_idx;
ALTER INDEX integration_installs_credential_secret_idx RENAME TO project_apps_credential_secret_idx;

ALTER TABLE secrets DROP CONSTRAINT secrets_kind_check;
ALTER TABLE secrets ADD CONSTRAINT secrets_kind_check
    CHECK (kind IN ('generic', 'oauth_token_set', 'slack_app_credentials', 'aws_credentials', 'github_app_credentials'));

ALTER TABLE actors DROP CONSTRAINT actors_provider_check;
ALTER TABLE actors ADD CONSTRAINT actors_provider_check
    CHECK (provider IN ('omnara', 'slack', 'app', 'external'));

ALTER TABLE project_apps
    ADD COLUMN name text,
    ADD COLUMN app_type text NOT NULL DEFAULT 'slack_thread'
        CHECK (app_type IN ('slack_thread', 'discord_thread', 'github_pr')),
    ADD COLUMN settings jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(settings) = 'object'),
    ADD COLUMN setup_revision bigint NOT NULL DEFAULT 1 CHECK (setup_revision > 0),
    ALTER COLUMN installed_by_user_id DROP NOT NULL,
    ALTER COLUMN provider_tenant_id DROP NOT NULL,
    ALTER COLUMN provider_account_ref DROP NOT NULL;

WITH names AS (
    SELECT id, row_number() OVER (PARTITION BY project_id ORDER BY (deleted_at IS NOT NULL), created_at, id) AS ordinal
    FROM project_apps
)
UPDATE project_apps app
SET name = CASE WHEN names.ordinal = 1 THEN 'slack' ELSE 'slack-' || names.ordinal::text END,
    settings = jsonb_build_object('launcher', jsonb_build_object(
        'trigger', 'mention', 'scope_kind', 'workspace', 'scope_ref', app.provider_tenant_id,
        'slots', jsonb_build_array(jsonb_build_object('key', 'default', 'agent_profile_id', app.agent_profile_id))))
FROM names WHERE names.id = app.id;

ALTER TABLE project_apps
    ALTER COLUMN name SET NOT NULL,
    ADD CHECK (name ~ '^[A-Za-z][A-Za-z0-9-]{0,31}$'),
    ALTER COLUMN app_type DROP DEFAULT,
    DROP COLUMN agent_profile_id,
    DROP COLUMN agent_id,
    DROP COLUMN integration_kind,
    DROP COLUMN connection_mode;
ALTER TABLE project_apps DROP CONSTRAINT integration_installs_state_check;
UPDATE project_apps SET state = 'disconnected' WHERE state = 'disabled' OR deleted_at IS NOT NULL;
ALTER TABLE project_apps ADD CONSTRAINT project_apps_state_check CHECK (state IN ('active', 'disconnected'));
ALTER TABLE project_apps ADD CHECK ((provider_tenant_id IS NULL) = (provider_account_ref IS NULL));
ALTER TABLE project_apps ADD CHECK (state <> 'active' OR
    (provider_tenant_id IS NOT NULL AND credential_secret_id IS NOT NULL AND installed_by_user_id IS NOT NULL));
ALTER TABLE project_apps DROP COLUMN provider;
CREATE UNIQUE INDEX project_apps_name_idx ON project_apps(project_id, name) WHERE deleted_at IS NULL;
CREATE INDEX project_apps_type_identity_idx ON project_apps(app_type, provider_tenant_id, provider_account_ref)
    WHERE deleted_at IS NULL AND provider_tenant_id IS NOT NULL;
CREATE INDEX project_apps_active_type_idx ON project_apps(app_type, id)
    WHERE state = 'active' AND deleted_at IS NULL;

-- +goose StatementBegin
CREATE FUNCTION project_apps_reject_identity_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (NEW.id, NEW.org_id, NEW.project_id, NEW.name, NEW.app_type, NEW.created_at)
        IS DISTINCT FROM (OLD.id, OLD.org_id, OLD.project_id, OLD.name, OLD.app_type, OLD.created_at)
       OR (OLD.provider_tenant_id IS NOT NULL AND
           (NEW.provider_tenant_id, NEW.provider_account_ref) IS DISTINCT FROM
           (OLD.provider_tenant_id, OLD.provider_account_ref)) THEN
        RAISE EXCEPTION 'project app identity is immutable' USING ERRCODE = '25006';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER project_apps_identity_immutable BEFORE UPDATE ON project_apps
FOR EACH ROW EXECUTE FUNCTION project_apps_reject_identity_change();

ALTER TABLE integration_targets
    DROP COLUMN target_ref, -- Drops the obsolete alias index and nonempty check too.
    ADD COLUMN is_tool_context boolean NOT NULL DEFAULT false,
    ADD COLUMN selection_slot text,
    ADD CHECK (selection_slot IS NULL OR selection_slot <> '');

DROP INDEX integration_targets_active_provider_ref_idx;
CREATE UNIQUE INDEX integration_targets_active_agent_address_idx
    ON integration_targets(project_id, agent_id, app_id, provider_ref_kind, provider_ref)
    WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX integration_targets_selection_idx
    ON integration_targets(project_id, app_id, provider_ref_kind, provider_ref, selection_slot)
    WHERE selection_slot IS NOT NULL;
-- Retired sending contexts must still prevent assigning a second context.
CREATE UNIQUE INDEX integration_targets_tool_context_idx
    ON integration_targets(project_id, agent_id, app_id) WHERE is_tool_context;

-- Go46 temporarily disables this guard to assign existing Slack sending contexts.
-- +goose StatementBegin
CREATE FUNCTION integration_targets_reject_tool_context_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.is_tool_context IS DISTINCT FROM OLD.is_tool_context
       OR (OLD.is_tool_context AND
           (NEW.project_id, NEW.agent_id, NEW.app_id, NEW.provider_ref_kind, NEW.provider_ref)
           IS DISTINCT FROM
           (OLD.project_id, OLD.agent_id, OLD.app_id, OLD.provider_ref_kind, OLD.provider_ref)) THEN
        RAISE EXCEPTION 'integration target tool context is immutable' USING ERRCODE = '25006';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER integration_targets_tool_context_immutable BEFORE UPDATE ON integration_targets
FOR EACH ROW EXECUTE FUNCTION integration_targets_reject_tool_context_change();

CREATE INDEX integration_targets_conversation_idx
    ON integration_targets(project_id, app_id, provider_ref_kind, provider_ref);

CREATE TABLE app_subscriptions (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    project_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    app_id uuid NOT NULL,
    subscription_type text NOT NULL CHECK (subscription_type <> ''),
    scope_kind text NOT NULL CHECK (scope_kind <> ''),
    scope_ref text NOT NULL CHECK (scope_ref <> ''),
    events text[] NOT NULL CHECK (cardinality(events) > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (project_id, agent_id) REFERENCES agents(project_id, id),
    FOREIGN KEY (project_id, app_id) REFERENCES project_apps(project_id, id)
);
CREATE UNIQUE INDEX app_subscriptions_conversation_idx
    ON app_subscriptions(project_id, agent_id, app_id, subscription_type, scope_kind, scope_ref);
CREATE INDEX app_subscriptions_routing_idx
    ON app_subscriptions(project_id, app_id, scope_kind, scope_ref);
CREATE INDEX app_subscriptions_app_list_idx
    ON app_subscriptions(project_id, app_id, created_at DESC, id DESC);

CREATE TABLE app_runtime (
    project_id uuid NOT NULL,
    app_id uuid NOT NULL,
    runtime_key text NOT NULL CHECK (octet_length(runtime_key) BETWEEN 1 AND 128),
    setup_revision bigint NOT NULL,
    credential_version_id uuid NOT NULL REFERENCES secret_versions(id) ON DELETE CASCADE,
    checkpoint jsonb CHECK (jsonb_typeof(checkpoint) = 'object' AND octet_length(checkpoint::text) <= 65536),
    claim_token uuid,
    claim_expires_at timestamptz,
    available_at timestamptz NOT NULL DEFAULT now(),
    last_error text CHECK (octet_length(last_error) <= 4096),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, app_id, runtime_key),
    FOREIGN KEY (project_id, app_id) REFERENCES project_apps(project_id, id),
    CHECK ((claim_token IS NULL) = (claim_expires_at IS NULL))
);
CREATE INDEX app_runtime_credential_idx ON app_runtime(credential_version_id);

CREATE TABLE integration_inbox (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    project_id uuid NOT NULL,
    app_id uuid NOT NULL,
    receipt_key text NOT NULL CHECK (octet_length(receipt_key) BETWEEN 1 AND 512),
    payload bytea NOT NULL CHECK (octet_length(payload) BETWEEN 1 AND 1048576),
    source text NOT NULL DEFAULT 'provider' CHECK (source IN ('provider', 'scheduled')),
    CHECK (source = 'provider' OR events IS NULL),
    events jsonb CHECK (jsonb_typeof(events) = 'array' AND octet_length(events::text) <= 262144),
    plan jsonb CHECK (jsonb_typeof(plan) = 'object' AND octet_length(plan::text) <= 262144),
    progress jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(progress) = 'object' AND octet_length(progress::text) <= 262144),
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'processing', 'completed', 'failed')),
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count BETWEEN 0 AND 8),
    available_at timestamptz NOT NULL DEFAULT now(),
    claim_token uuid,
    claim_expires_at timestamptz,
    last_error text CHECK (octet_length(last_error) <= 4096),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    CHECK ((state = 'processing') = (claim_token IS NOT NULL AND claim_expires_at IS NOT NULL)),
    CHECK ((claim_token IS NULL) = (claim_expires_at IS NULL)),
    CHECK ((state IN ('completed', 'failed')) = (completed_at IS NOT NULL)),
    FOREIGN KEY (project_id, app_id) REFERENCES project_apps(project_id, id),
    UNIQUE (project_id, app_id, receipt_key)
);

CREATE INDEX integration_inbox_ready_idx ON integration_inbox(available_at, id)
    WHERE state = 'pending';
CREATE INDEX integration_inbox_expired_idx ON integration_inbox(claim_expires_at, id)
    WHERE state = 'processing';
CREATE INDEX integration_inbox_terminal_idx ON integration_inbox(completed_at, id)
    WHERE state IN ('completed', 'failed');
CREATE INDEX integration_inbox_app_ready_idx
    ON integration_inbox(project_id, app_id, available_at, id) WHERE state = 'pending';
CREATE INDEX integration_inbox_selection_idx ON integration_inbox USING gin
    ((jsonb_path_query_array(plan, '$.*.selection')) jsonb_path_ops)
    WHERE plan IS NOT NULL AND state IN ('pending', 'processing');

CREATE TABLE app_states (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    project_id uuid NOT NULL,
    app_id uuid NOT NULL,
    kind text NOT NULL CHECK (octet_length(kind) BETWEEN 1 AND 128),
    key text NOT NULL CHECK (octet_length(key) BETWEEN 1 AND 512),
    scope_kind text CHECK (octet_length(scope_kind) BETWEEN 1 AND 128),
    scope_ref text CHECK (octet_length(scope_ref) BETWEEN 1 AND 2048),
    data jsonb NOT NULL CHECK (jsonb_typeof(data) = 'object' AND octet_length(data::text) <= 2097152),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    expires_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    CHECK ((scope_kind IS NULL) = (scope_ref IS NULL)),
    FOREIGN KEY (project_id, app_id) REFERENCES project_apps(project_id, id),
    UNIQUE (project_id, app_id, kind, key)
);
CREATE INDEX app_states_scope_idx
    ON app_states(project_id, app_id, kind, scope_kind, scope_ref, expires_at, id)
    WHERE scope_kind IS NOT NULL;
CREATE INDEX app_states_expiry_idx ON app_states(kind, expires_at, id)
    WHERE expires_at IS NOT NULL;

ALTER TABLE org_resource_limit_overrides
    ADD COLUMN max_active_project_apps_per_project bigint CHECK (max_active_project_apps_per_project >= 0),
    ADD COLUMN max_active_app_subscriptions_per_agent bigint CHECK (max_active_app_subscriptions_per_agent >= 0);

CREATE OR REPLACE VIEW default_resource_limits AS
SELECT
    1000::bigint AS max_active_projects_per_org,
    10000::bigint AS max_pending_org_invitations_per_org,
    10000::bigint AS max_active_org_api_keys_per_org,
    10000::bigint AS max_active_tenant_model_provider_configs_per_org,
    10000::bigint AS max_active_configured_models_per_provider,
    10000000::bigint AS max_agent_configs_per_project,
    10000::bigint AS max_active_agent_profiles_per_project,
    10000::bigint AS max_active_agents_per_project,
    10000::bigint AS max_active_tenant_secrets_per_owner,
    10000::bigint AS max_active_skills_per_owner,
    10000::bigint AS max_active_tenant_machine_pools_per_org,
    10000::bigint AS max_live_machines_per_org,
    20::bigint AS max_active_byo_daemon_tokens_per_machine,
    32::bigint AS max_non_terminal_processes_per_agent,
    1000::bigint AS max_active_cron_triggers_per_project,
    1000::bigint AS max_active_project_apps_per_project,
    1024::bigint AS max_active_app_subscriptions_per_agent;

CREATE OR REPLACE VIEW effective_resource_limits AS
SELECT
    orgs.id AS org_id,
    coalesce(overrides.max_active_projects_per_org, defaults.max_active_projects_per_org) AS max_active_projects_per_org,
    coalesce(overrides.max_pending_org_invitations_per_org, defaults.max_pending_org_invitations_per_org) AS max_pending_org_invitations_per_org,
    coalesce(overrides.max_active_org_api_keys_per_org, defaults.max_active_org_api_keys_per_org) AS max_active_org_api_keys_per_org,
    coalesce(overrides.max_active_tenant_model_provider_configs_per_org, defaults.max_active_tenant_model_provider_configs_per_org) AS max_active_tenant_model_provider_configs_per_org,
    coalesce(overrides.max_active_configured_models_per_provider, defaults.max_active_configured_models_per_provider) AS max_active_configured_models_per_provider,
    coalesce(overrides.max_agent_configs_per_project, defaults.max_agent_configs_per_project) AS max_agent_configs_per_project,
    coalesce(overrides.max_active_agent_profiles_per_project, defaults.max_active_agent_profiles_per_project) AS max_active_agent_profiles_per_project,
    coalesce(overrides.max_active_agents_per_project, defaults.max_active_agents_per_project) AS max_active_agents_per_project,
    coalesce(overrides.max_active_tenant_secrets_per_owner, defaults.max_active_tenant_secrets_per_owner) AS max_active_tenant_secrets_per_owner,
    coalesce(overrides.max_active_skills_per_owner, defaults.max_active_skills_per_owner) AS max_active_skills_per_owner,
    coalesce(overrides.max_active_tenant_machine_pools_per_org, defaults.max_active_tenant_machine_pools_per_org) AS max_active_tenant_machine_pools_per_org,
    coalesce(overrides.max_live_machines_per_org, defaults.max_live_machines_per_org) AS max_live_machines_per_org,
    coalesce(overrides.max_active_byo_daemon_tokens_per_machine, defaults.max_active_byo_daemon_tokens_per_machine) AS max_active_byo_daemon_tokens_per_machine,
    coalesce(overrides.max_non_terminal_processes_per_agent, defaults.max_non_terminal_processes_per_agent) AS max_non_terminal_processes_per_agent,
    coalesce(overrides.max_active_cron_triggers_per_project, defaults.max_active_cron_triggers_per_project) AS max_active_cron_triggers_per_project,
    coalesce(overrides.max_active_project_apps_per_project, defaults.max_active_project_apps_per_project) AS max_active_project_apps_per_project,
    coalesce(overrides.max_active_app_subscriptions_per_agent, defaults.max_active_app_subscriptions_per_agent) AS max_active_app_subscriptions_per_agent
FROM orgs
CROSS JOIN default_resource_limits AS defaults
LEFT JOIN org_resource_limit_overrides AS overrides ON overrides.org_id = orgs.id
WHERE orgs.deleted_at IS NULL;

UPDATE agents SET integration_target_id = NULL WHERE integration_target_id IS NOT NULL;

ALTER TABLE agents
    ADD COLUMN interaction_handler_key text,
    ADD COLUMN interaction_handler_args jsonb,
    ADD CHECK (
        (integration_target_id IS NULL AND interaction_handler_key IS NULL AND interaction_handler_args IS NULL)
        OR (integration_target_id IS NOT NULL AND interaction_handler_key IS NOT NULL
            AND interaction_handler_key <> '' AND interaction_handler_args IS NOT NULL
            AND jsonb_typeof(interaction_handler_args) = 'object'));
ALTER TABLE agent_interactions
    ADD COLUMN destination jsonb CHECK (destination IS NULL OR (jsonb_typeof(destination) = 'object' AND octet_length(destination::text) <= 4096)),
    ADD COLUMN presentation_receipt jsonb CHECK (presentation_receipt IS NULL OR (destination IS NOT NULL AND jsonb_typeof(presentation_receipt) = 'object' AND octet_length(presentation_receipt::text) <= 16384)),
    ADD COLUMN presentation_attempted_at timestamptz
        CHECK (presentation_attempted_at IS NULL OR destination IS NOT NULL);

CREATE INDEX agent_interactions_pending_presentation_idx
    ON agent_interactions ((destination ->> 'app_type'), created_at, agent_id, id)
    WHERE state = 'open' AND destination IS NOT NULL
      AND presentation_attempted_at IS NULL AND presentation_receipt IS NULL;

CREATE OR REPLACE VIEW agent_interaction_read_projection AS
SELECT interaction.id,
       tool_call.project_id,
       interaction.agent_id,
       tool_call.turn_id,
       tool_call.model_call_context_id,
       interaction.tool_call_id,
       tool_call.provider_call_id,
       interaction.interaction_kind,
       interaction.state,
       interaction.request,
       interaction.resolution,
       interaction.resolved_by_input_id,
       interaction.created_at,
       interaction.resolved_at,
       interaction.destination,
       interaction.presentation_receipt
FROM agent_interactions interaction
JOIN tool_call_read_projection tool_call ON tool_call.agent_id = interaction.agent_id
  AND tool_call.id = interaction.tool_call_id;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION enforce_agent_interaction_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'agent_interactions are immutable'
            USING ERRCODE = '25006';
    END IF;
    IF TG_OP = 'INSERT' THEN
        IF NEW.state <> 'open' OR NEW.presentation_receipt IS NOT NULL
           OR NEW.presentation_attempted_at IS NOT NULL THEN
            RAISE EXCEPTION 'agent_interactions must be inserted in open state'
                USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;

    IF OLD.state = 'open' AND OLD.destination IS NOT NULL
       AND OLD.presentation_attempted_at IS NULL AND NEW.presentation_attempted_at IS NOT NULL
       AND OLD.presentation_receipt IS NULL
       AND (to_jsonb(OLD) - 'presentation_attempted_at') = (to_jsonb(NEW) - 'presentation_attempted_at') THEN
        RETURN NEW;
    END IF;

    -- Record late confirmed sends after cancellation so cleanup can dismiss them.
    IF OLD.presentation_receipt IS NULL AND NEW.presentation_receipt IS NOT NULL
       AND NEW.destination IS NOT NULL
       AND (to_jsonb(OLD) - 'presentation_receipt') = (to_jsonb(NEW) - 'presentation_receipt') THEN
        RETURN NEW;
    END IF;

    IF OLD.state <> 'open' THEN
        RAISE EXCEPTION 'terminal agent_interaction is immutable'
            USING ERRCODE = '25006';
    END IF;
    IF NEW.state NOT IN ('resolved', 'canceled') THEN
        RAISE EXCEPTION 'agent_interaction must transition from open to a terminal state'
            USING ERRCODE = '25006';
    END IF;
    IF OLD.id IS DISTINCT FROM NEW.id
       OR OLD.agent_id IS DISTINCT FROM NEW.agent_id
       OR OLD.tool_call_id IS DISTINCT FROM NEW.tool_call_id
       OR OLD.interaction_kind IS DISTINCT FROM NEW.interaction_kind
       OR OLD.request IS DISTINCT FROM NEW.request
       OR OLD.created_at IS DISTINCT FROM NEW.created_at
       OR OLD.destination IS DISTINCT FROM NEW.destination
       OR OLD.presentation_attempted_at IS DISTINCT FROM NEW.presentation_attempted_at
       OR OLD.presentation_receipt IS DISTINCT FROM NEW.presentation_receipt THEN
        RAISE EXCEPTION 'agent_interaction lineage is immutable'
            USING ERRCODE = '25006';
    END IF;

    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- check1 is migration 20's profile/agent exclusivity; target_check replaces it.
ALTER TABLE cron_triggers
    DROP CONSTRAINT cron_triggers_check1,
    DROP CONSTRAINT cron_triggers_message_template_check,
    DROP CONSTRAINT cron_triggers_profile_delivery_mode_check,
    ALTER COLUMN message_template DROP NOT NULL,
    ADD COLUMN app_id uuid,
    ADD COLUMN app_settings jsonb,
    ADD COLUMN last_app_receipt_id uuid,
    ADD FOREIGN KEY (project_id, app_id) REFERENCES project_apps(project_id, id),
    ADD CONSTRAINT cron_triggers_target_check CHECK (
        num_nonnulls(app_id, agent_profile_id, agent_id) = 1
    ),
    ADD CONSTRAINT cron_triggers_message_template_check CHECK (
        (app_id IS NOT NULL AND message_template IS NULL)
        OR (app_id IS NULL AND message_template IS NOT NULL AND message_template <> '')
    ),
    ADD CONSTRAINT cron_triggers_app_settings_check CHECK (
        (app_id IS NULL AND app_settings IS NULL AND last_app_receipt_id IS NULL)
        OR (app_id IS NOT NULL AND app_settings IS NOT NULL
            AND jsonb_typeof(app_settings) = 'object'
            AND octet_length(app_settings::text) <= 262144)
    ),
    ADD CONSTRAINT cron_triggers_target_delivery_mode_check CHECK (
        agent_id IS NOT NULL OR delivery_mode = 'queued'
    );
CREATE INDEX cron_triggers_app_idx ON cron_triggers(project_id, app_id)
    WHERE app_id IS NOT NULL AND deleted_at IS NULL;
