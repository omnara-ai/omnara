-- +goose Up

-- Goose commits each migration separately: reject unfinished work before SQL46
-- changes the schema needed by the old release to stop it. Go47 rechecks under lock.
-- Keep this legacy-policy preflight in sync with Go47's frozen translator.
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
        RAISE EXCEPTION 'integration cutover requires maintenance: stop outstanding work and interactions through the old release, then stop writers';
    END IF;
    SELECT agent.id INTO unfinished_agent FROM agents agent
    JOIN LATERAL (SELECT id FROM agent_turns WHERE agent_id=agent.id ORDER BY turn_sequence DESC LIMIT 1) latest ON true
    WHERE EXISTS (SELECT 1 FROM agent_continuable_model_contexts(agent.project_id,agent.id) context WHERE context.turn_id=latest.id)
       OR agent_has_incomplete_tool_batch(agent.project_id,agent.id)
       OR EXISTS (SELECT 1 FROM agent_next_model_work(agent.project_id,agent.id) frontier WHERE frontier.turn_id=latest.id)
    LIMIT 1;
    IF unfinished_agent IS NOT NULL THEN
        RAISE EXCEPTION 'integration cutover: agent % still has continuable work; stop it through the old release', unfinished_agent;
    END IF;
    SELECT config.id, tool.key INTO conflicting_config, conflicting_tool
    FROM agent_configs config CROSS JOIN LATERAL jsonb_each(coalesce(nullif(config.compiled_definition->'tools','null'::jsonb),'{}'::jsonb)) tool
    WHERE tool.value->>'type' = 'custom'
      AND (starts_with(tool.key, 'int__') OR tool.key IN ('list_interaction_handlers','set_interaction_handler'))
    LIMIT 1;
    IF conflicting_config IS NOT NULL THEN
        RAISE EXCEPTION 'config % custom tool % conflicts with a new integration built-in; historical configs require repair before cutover', conflicting_config, conflicting_tool;
    END IF;
    SELECT id INTO unsupported_install FROM integration_installs
    WHERE provider <> 'slack' OR agent_id IS NOT NULL OR agent_profile_id IS NULL
       OR connection_mode <> 'webhook'
       OR (state='active' AND deleted_at IS NULL AND credential_secret_id IS NULL) LIMIT 1;
    IF unsupported_install IS NOT NULL THEN
        RAISE EXCEPTION 'install % is not a profile-bound Slack webhook setup; review and repair before integration cutover', unsupported_install;
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
        RAISE EXCEPTION 'project % config % has an unmappable legacy send policy; repair before integration cutover', policy_project, policy_config;
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
        RAISE EXCEPTION 'agent % has several live targets through one Slack integration: %. Review these destinations before cutover; no target has been removed or broadened', ambiguous_agent, ambiguous_targets;
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
        RAISE EXCEPTION 'invalid Slack target %; repair before integration cutover', invalid_target;
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
        RAISE EXCEPTION 'project % needs additional config quota for Slack successors; raise the existing org override before integration cutover', full_project;
    END IF;
END;
$$;
-- +goose StatementEnd

ALTER TABLE integration_installs RENAME TO project_integrations;
ALTER TABLE integration_targets RENAME COLUMN integration_install_id TO integration_id;
DROP INDEX integration_installs_provider_tenant_account_idx;
ALTER INDEX integration_installs_last_oauth_flow_id_idx RENAME TO project_integrations_last_oauth_flow_id_idx;
ALTER INDEX integration_installs_credential_secret_idx RENAME TO project_integrations_credential_secret_idx;

ALTER TABLE secrets DROP CONSTRAINT secrets_kind_check;
ALTER TABLE secrets ADD CONSTRAINT secrets_kind_check
    CHECK (kind IN ('generic', 'oauth_token_set', 'slack_app_credentials', 'aws_credentials', 'github_app_credentials'));

ALTER TABLE actors DROP CONSTRAINT actors_provider_check;
ALTER TABLE actors ADD CONSTRAINT actors_provider_check
    CHECK (provider IN ('omnara', 'slack', 'integration', 'external'));

ALTER TABLE project_integrations
    ADD COLUMN name text,
    ADD COLUMN integration_type text NOT NULL DEFAULT 'slack_thread'
        CHECK (integration_type IN ('slack_thread', 'discord_thread', 'github_pr')),
    ADD COLUMN settings jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(settings) = 'object'),
    ADD COLUMN setup_revision bigint NOT NULL DEFAULT 1 CHECK (setup_revision > 0),
    ALTER COLUMN installed_by_user_id DROP NOT NULL,
    ALTER COLUMN provider_tenant_id DROP NOT NULL,
    ALTER COLUMN provider_account_ref DROP NOT NULL;

WITH names AS (
    SELECT id, row_number() OVER (PARTITION BY project_id ORDER BY (deleted_at IS NOT NULL), created_at, id) AS ordinal
    FROM project_integrations
)
UPDATE project_integrations integration
SET name = CASE WHEN names.ordinal = 1 THEN 'slack' ELSE 'slack-' || names.ordinal::text END,
    settings = jsonb_build_object('launcher', jsonb_build_object(
        'trigger', 'mention', 'scope_kind', 'workspace', 'scope_ref', integration.provider_tenant_id,
        'slots', jsonb_build_array(jsonb_build_object('key', 'default', 'agent_profile_id', integration.agent_profile_id))))
FROM names WHERE names.id = integration.id;

ALTER TABLE project_integrations
    ALTER COLUMN name SET NOT NULL,
    ADD CHECK (name ~ '^[A-Za-z][A-Za-z0-9-]{0,31}$'),
    ALTER COLUMN integration_type DROP DEFAULT,
    DROP COLUMN agent_profile_id,
    DROP COLUMN agent_id,
    DROP COLUMN integration_kind,
    DROP COLUMN connection_mode;
ALTER TABLE project_integrations DROP CONSTRAINT integration_installs_state_check;
UPDATE project_integrations SET state = 'disconnected' WHERE state = 'disabled' OR deleted_at IS NOT NULL;
ALTER TABLE project_integrations ADD CONSTRAINT project_integrations_state_check CHECK (state IN ('active', 'disconnected'));
ALTER TABLE project_integrations ADD CHECK ((provider_tenant_id IS NULL) = (provider_account_ref IS NULL));
ALTER TABLE project_integrations ADD CHECK (state <> 'active' OR
    (provider_tenant_id IS NOT NULL AND credential_secret_id IS NOT NULL AND installed_by_user_id IS NOT NULL));
ALTER TABLE project_integrations DROP COLUMN provider;
CREATE UNIQUE INDEX project_integrations_name_idx ON project_integrations(project_id, name) WHERE deleted_at IS NULL;
CREATE INDEX project_integrations_type_identity_idx ON project_integrations(integration_type, provider_tenant_id, provider_account_ref)
    WHERE deleted_at IS NULL AND provider_tenant_id IS NOT NULL;
CREATE INDEX project_integrations_active_type_idx ON project_integrations(integration_type, id)
    WHERE state = 'active' AND deleted_at IS NULL;

-- +goose StatementBegin
CREATE FUNCTION project_integrations_reject_identity_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (NEW.id, NEW.org_id, NEW.project_id, NEW.name, NEW.integration_type, NEW.created_at)
        IS DISTINCT FROM (OLD.id, OLD.org_id, OLD.project_id, OLD.name, OLD.integration_type, OLD.created_at)
       OR (OLD.provider_tenant_id IS NOT NULL AND
           (NEW.provider_tenant_id, NEW.provider_account_ref) IS DISTINCT FROM
           (OLD.provider_tenant_id, OLD.provider_account_ref)) THEN
        RAISE EXCEPTION 'project integration identity is immutable' USING ERRCODE = '25006';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER project_integrations_identity_immutable BEFORE UPDATE ON project_integrations
FOR EACH ROW EXECUTE FUNCTION project_integrations_reject_identity_change();

ALTER TABLE integration_targets
    DROP COLUMN target_ref, -- Drops the obsolete alias index and nonempty check too.
    ADD COLUMN selection_slot text,
    ADD CHECK (selection_slot IS NULL OR selection_slot <> '');

DROP INDEX integration_targets_active_provider_ref_idx;
CREATE UNIQUE INDEX integration_targets_active_agent_address_idx
    ON integration_targets(project_id, agent_id, integration_id, provider_ref_kind, provider_ref)
    WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX integration_targets_selection_idx
    ON integration_targets(project_id, integration_id, provider_ref_kind, provider_ref, selection_slot)
    WHERE selection_slot IS NOT NULL;
CREATE INDEX integration_targets_conversation_idx
    ON integration_targets(project_id, integration_id, provider_ref_kind, provider_ref);

-- Rename surviving install constraints, including PostgreSQL 18 NOT NULL
-- constraints. Constraint-owned indexes follow their constraint names.
-- +goose StatementBegin
DO $$
DECLARE constraint_row record; new_name text;
BEGIN
    FOR constraint_row IN
        SELECT conrelid, conname FROM pg_constraint
        WHERE conrelid IN ('project_integrations'::regclass, 'integration_targets'::regclass)
          AND (conname LIKE 'integration_installs_%' OR conname LIKE '%integration_install_id%')
    LOOP
        new_name := replace(replace(constraint_row.conname,
            'integration_installs', 'project_integrations'), 'integration_install_id', 'integration_id');
        IF new_name <> constraint_row.conname THEN
            EXECUTE format('ALTER TABLE %s RENAME CONSTRAINT %I TO %I',
                           constraint_row.conrelid::regclass, constraint_row.conname, new_name);
        END IF;
    END LOOP;
END;
$$;
-- +goose StatementEnd

CREATE TABLE integration_subscriptions (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    project_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    integration_id uuid NOT NULL,
    scope_kind text NOT NULL CHECK (scope_kind <> ''),
    scope_ref text NOT NULL CHECK (scope_ref <> ''),
    created_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (project_id, agent_id) REFERENCES agents(project_id, id),
    FOREIGN KEY (project_id, integration_id) REFERENCES project_integrations(project_id, id)
);
CREATE UNIQUE INDEX integration_subscriptions_conversation_idx
    ON integration_subscriptions(project_id, agent_id, integration_id, scope_kind, scope_ref);
CREATE INDEX integration_subscriptions_routing_idx
    ON integration_subscriptions(project_id, integration_id, scope_kind, scope_ref);
CREATE INDEX integration_subscriptions_integration_list_idx
    ON integration_subscriptions(project_id, integration_id, created_at DESC, id DESC);

CREATE TABLE integration_runtime (
    project_id uuid NOT NULL,
    integration_id uuid NOT NULL,
    runtime_key text NOT NULL CHECK (octet_length(runtime_key) BETWEEN 1 AND 128),
    setup_revision bigint NOT NULL,
    credential_version_id uuid NOT NULL REFERENCES secret_versions(id) ON DELETE CASCADE,
    checkpoint jsonb CHECK (jsonb_typeof(checkpoint) = 'object' AND octet_length(checkpoint::text) <= 65536),
    claim_token uuid,
    claim_expires_at timestamptz,
    available_at timestamptz NOT NULL DEFAULT now(),
    last_error text CHECK (octet_length(last_error) <= 4096),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, integration_id, runtime_key),
    FOREIGN KEY (project_id, integration_id) REFERENCES project_integrations(project_id, id),
    CHECK ((claim_token IS NULL) = (claim_expires_at IS NULL))
);
CREATE INDEX integration_runtime_credential_idx ON integration_runtime(credential_version_id);

CREATE TABLE integration_inbox (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    project_id uuid NOT NULL,
    integration_id uuid NOT NULL,
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
    FOREIGN KEY (project_id, integration_id) REFERENCES project_integrations(project_id, id),
    UNIQUE (project_id, integration_id, receipt_key)
);

CREATE INDEX integration_inbox_ready_idx ON integration_inbox(available_at, id)
    WHERE state = 'pending';
CREATE INDEX integration_inbox_expired_idx ON integration_inbox(claim_expires_at, id)
    WHERE state = 'processing';
CREATE INDEX integration_inbox_terminal_idx ON integration_inbox(completed_at, id)
    WHERE state IN ('completed', 'failed');
CREATE INDEX integration_inbox_integration_ready_idx
    ON integration_inbox(project_id, integration_id, available_at, id) WHERE state = 'pending';
CREATE INDEX integration_inbox_selection_idx ON integration_inbox USING gin
    ((jsonb_path_query_array(plan, '$.*.selection')) jsonb_path_ops)
    WHERE plan IS NOT NULL AND state IN ('pending', 'processing');

CREATE TABLE integration_states (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    project_id uuid NOT NULL,
    integration_id uuid NOT NULL,
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
    FOREIGN KEY (project_id, integration_id) REFERENCES project_integrations(project_id, id),
    UNIQUE (project_id, integration_id, kind, key)
);
CREATE INDEX integration_states_scope_idx
    ON integration_states(project_id, integration_id, kind, scope_kind, scope_ref, expires_at, id)
    WHERE scope_kind IS NOT NULL;
CREATE INDEX integration_states_expiry_idx ON integration_states(kind, expires_at, id)
    WHERE expires_at IS NOT NULL;

ALTER TABLE org_resource_limit_overrides
    ADD COLUMN max_active_project_integrations_per_project bigint CHECK (max_active_project_integrations_per_project >= 0),
    ADD COLUMN max_active_integration_subscriptions_per_agent bigint CHECK (max_active_integration_subscriptions_per_agent >= 0);

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
    1000::bigint AS max_active_project_integrations_per_project,
    1024::bigint AS max_active_integration_subscriptions_per_agent;

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
    coalesce(overrides.max_active_project_integrations_per_project, defaults.max_active_project_integrations_per_project) AS max_active_project_integrations_per_project,
    coalesce(overrides.max_active_integration_subscriptions_per_agent, defaults.max_active_integration_subscriptions_per_agent) AS max_active_integration_subscriptions_per_agent
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
    ON agent_interactions ((destination ->> 'integration_type'), created_at, agent_id, id)
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
    ADD COLUMN integration_id uuid,
    ADD COLUMN integration_settings jsonb,
    ADD COLUMN last_integration_receipt_id uuid,
    ADD FOREIGN KEY (project_id, integration_id) REFERENCES project_integrations(project_id, id),
    ADD CONSTRAINT cron_triggers_target_check CHECK (
        num_nonnulls(integration_id, agent_profile_id, agent_id) = 1
    ),
    ADD CONSTRAINT cron_triggers_message_template_check CHECK (
        (integration_id IS NOT NULL AND message_template IS NULL)
        OR (integration_id IS NULL AND message_template IS NOT NULL AND message_template <> '')
    ),
    ADD CONSTRAINT cron_triggers_integration_settings_check CHECK (
        (integration_id IS NULL AND integration_settings IS NULL AND last_integration_receipt_id IS NULL)
        OR (integration_id IS NOT NULL AND integration_settings IS NOT NULL
            AND jsonb_typeof(integration_settings) = 'object'
            AND octet_length(integration_settings::text) <= 262144)
    ),
    ADD CONSTRAINT cron_triggers_target_delivery_mode_check CHECK (
        agent_id IS NOT NULL OR delivery_mode = 'queued'
    );
CREATE INDEX cron_triggers_integration_idx ON cron_triggers(project_id, integration_id)
    WHERE integration_id IS NOT NULL AND deleted_at IS NULL;
