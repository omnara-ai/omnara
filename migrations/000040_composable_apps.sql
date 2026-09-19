-- +goose Up

-- Refuse before renaming anything: the old release must remain usable to stop
-- unfinished work. Go41 repeats these checks under its rewrite lock because
-- Goose commits each numbered migration separately. All writers must be stopped.
-- +goose StatementBegin
DO $$
DECLARE conflicting_config uuid; conflicting_tool text; unfinished_agent uuid;
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
    WHERE tool.value->>'type' = 'custom' AND tool.key IN ('slack_read','slack_post_message',
        'github_read','github_discussion_comment','github_inline_comment','github_reply',
        'discord_read','discord_post_message','list_interaction_destinations','set_interaction_destination') LIMIT 1;
    IF conflicting_config IS NOT NULL THEN
        RAISE EXCEPTION 'config % custom tool % conflicts with a new app built-in; historical configs require repair before cutover', conflicting_config, conflicting_tool;
    END IF;
END;
$$;
-- +goose StatementEnd

-- This release uses a coordinated maintenance window. Existing provider account
-- and conversation identities survive; there is no rolling dual-write path.
ALTER TABLE integration_installs RENAME TO integration_connections;
ALTER TABLE integration_targets RENAME COLUMN integration_install_id TO integration_connection_id;
ALTER INDEX integration_installs_provider_tenant_account_idx RENAME TO integration_connections_provider_tenant_account_idx;
ALTER INDEX integration_installs_last_oauth_flow_id_idx RENAME TO integration_connections_last_oauth_flow_id_idx;
ALTER INDEX integration_installs_credential_secret_idx RENAME TO integration_connections_credential_secret_idx;

ALTER TABLE secrets DROP CONSTRAINT secrets_kind_check;
ALTER TABLE secrets ADD CONSTRAINT secrets_kind_check
    CHECK (kind IN ('generic', 'oauth_token_set', 'slack_app_credentials', 'aws_credentials', 'github_app_credentials'));

ALTER TABLE actors DROP CONSTRAINT actors_provider_check;
ALTER TABLE actors ADD CONSTRAINT actors_provider_check
    CHECK (provider IN ('omnara', 'slack', 'github', 'discord', 'external'));

-- App definitions are reviewed code. These rows are reusable project settings,
-- optionally indexed as launchers. Existing agents pin their resolved resources
-- in immutable configs rather than following edits to this row.
CREATE TABLE project_apps (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    project_id uuid NOT NULL REFERENCES projects(id),
    name text NOT NULL CHECK (name <> '' AND resource_name_storage_is_valid(name)),
    definition_id text NOT NULL CHECK (definition_id <> ''),
    settings jsonb NOT NULL CHECK (jsonb_typeof(settings) = 'object'),
    launch_connection_id uuid,
    launch_scope_kind text,
    launch_scope_ref text,
    enabled boolean NOT NULL DEFAULT true,
    deleted_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((launch_connection_id IS NULL AND launch_scope_kind IS NULL AND launch_scope_ref IS NULL)
        OR (launch_connection_id IS NOT NULL AND launch_scope_kind IS NOT NULL AND launch_scope_ref IS NOT NULL
            AND launch_scope_kind <> '' AND launch_scope_ref <> '')),
    FOREIGN KEY (project_id, launch_connection_id) REFERENCES integration_connections(project_id, id),
    UNIQUE (project_id, id)
);
CREATE UNIQUE INDEX project_apps_name_idx ON project_apps(project_id, name) WHERE deleted_at IS NULL;
CREATE INDEX project_apps_launch_idx ON project_apps(project_id, launch_connection_id, launch_scope_kind, launch_scope_ref)
    WHERE enabled AND deleted_at IS NULL AND launch_connection_id IS NOT NULL;

-- Capture the old Slack setup once, before removing connection-owned behavior.
-- Go41 fills the encoded public connection reference. Neither service may start
-- between these migrations. Existing conversation targets remain attribution.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM integration_connections
               WHERE provider <> 'slack' OR integration_kind <> 'agent_profile'
                  OR connection_mode <> 'webhook') THEN
        RAISE EXCEPTION 'unsupported legacy integration setup; resolve before app cutover';
    END IF;
END;
$$;
-- +goose StatementEnd
INSERT INTO project_apps(project_id, name, definition_id, settings,
    launch_connection_id, launch_scope_kind, launch_scope_ref, enabled, deleted_at, created_at, updated_at)
SELECT project_id, 'slack-' || id::text, 'omnara.slack',
    jsonb_build_object(
        'resource', jsonb_build_object('definition', 'omnara.slack',
            'tools', jsonb_build_object('slack_read', '{}'::jsonb, 'slack_post_message', '{}'::jsonb),
            'listener', jsonb_build_object('events', jsonb_build_array('message')),
            'interaction_handler', jsonb_build_object('definition', 'omnara.slack.interactions')),
        'launcher', jsonb_build_object('trigger', 'mention', 'scope_kind', 'workspace',
            'scope_ref', provider_tenant_id, 'slots', jsonb_build_array(
                jsonb_strip_nulls(jsonb_build_object('key', 'default',
                    'agent_profile_id', agent_profile_id, 'agent_id', agent_id))))),
    id, 'workspace', provider_tenant_id, state = 'active', deleted_at, created_at, updated_at
FROM integration_connections;

ALTER TABLE integration_connections
    DROP COLUMN agent_profile_id,
    DROP COLUMN agent_id,
    DROP COLUMN integration_kind,
    DROP COLUMN connection_mode;
ALTER TABLE integration_connections DROP CONSTRAINT integration_installs_provider_check;
ALTER TABLE integration_connections ADD CONSTRAINT integration_connections_provider_check
    CHECK (provider IN ('slack', 'github', 'discord'));

-- The existing global account identity resolves provider callbacks to one project.

-- Targets retain attribution and successful selection even after receiving is
-- disabled. They are neither a provider credential grant nor a subscription.
ALTER TABLE integration_targets
    ADD COLUMN routing_role text NOT NULL DEFAULT 'attribution'
        CHECK (routing_role IN ('attribution', 'selected', 'followed')),
    ADD COLUMN app_id uuid,
    ADD COLUMN selection_slot text,
    ADD FOREIGN KEY (project_id, app_id) REFERENCES project_apps(project_id, id),
    ADD CHECK ((routing_role = 'selected' AND app_id IS NOT NULL AND selection_slot IS NOT NULL)
        OR (routing_role <> 'selected' AND app_id IS NULL AND selection_slot IS NULL)),
    ADD CHECK (selection_slot IS NULL OR selection_slot <> '');

DROP INDEX integration_targets_active_provider_ref_idx;
CREATE UNIQUE INDEX integration_targets_active_agent_address_idx
    ON integration_targets(project_id, agent_id, integration_connection_id, provider_ref_kind, provider_ref)
    WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX integration_targets_selection_idx
    ON integration_targets(project_id, integration_connection_id, provider_ref_kind, provider_ref, app_id, selection_slot)
    WHERE routing_role = 'selected';
CREATE INDEX integration_targets_conversation_idx
    ON integration_targets(project_id, integration_connection_id, provider_ref_kind, provider_ref);

-- Config activation materializes receive subscriptions. A confirmed tool send
-- may add an exact follow, retaining its resource authority and tool provenance.
CREATE TABLE agent_listeners (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    project_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    connection_id uuid NOT NULL,
    resource_key text NOT NULL CHECK (resource_key <> ''),
    scope_kind text NOT NULL CHECK (scope_kind <> ''),
    scope_ref text NOT NULL CHECK (scope_ref <> ''),
    events text[] NOT NULL CHECK (cardinality(events) > 0),
    source_config_id uuid NOT NULL,
    tool_call_id uuid,
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (project_id, agent_id) REFERENCES agents(project_id, id),
    FOREIGN KEY (project_id, connection_id) REFERENCES integration_connections(project_id, id),
    FOREIGN KEY (project_id, source_config_id) REFERENCES agent_configs(project_id, id),
    FOREIGN KEY (agent_id, tool_call_id) REFERENCES tool_calls(agent_id, id)
);
-- One declared and one confirmed-follow subscription per resource/address.
-- Repeated posts into a followed thread must not consume additional listeners.
CREATE UNIQUE INDEX agent_listeners_resource_scope_idx
    ON agent_listeners(project_id, agent_id, resource_key, scope_kind, scope_ref, (tool_call_id IS NOT NULL));
CREATE INDEX agent_listeners_scope_idx ON agent_listeners(project_id, connection_id, scope_kind, scope_ref)
    WHERE active;
CREATE INDEX agent_listeners_agent_idx ON agent_listeners(project_id, agent_id);

-- Persistent transports own one bounded unit (a Discord shard today). There is
-- no app-owned runtime: the connection and credential revisions fence its owner.
CREATE TABLE integration_connection_runtime (
    project_id uuid NOT NULL,
    connection_id uuid NOT NULL,
    runtime_key text NOT NULL CHECK (octet_length(runtime_key) BETWEEN 1 AND 128),
    connection_updated_at timestamptz NOT NULL,
    credential_version_id uuid NOT NULL REFERENCES secret_versions(id) ON DELETE CASCADE,
    checkpoint jsonb CHECK (jsonb_typeof(checkpoint) = 'object' AND octet_length(checkpoint::text) <= 65536),
    claim_token uuid,
    claim_expires_at timestamptz,
    available_at timestamptz NOT NULL DEFAULT now(),
    last_error text CHECK (octet_length(last_error) <= 4096),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, connection_id, runtime_key),
    FOREIGN KEY (project_id, connection_id) REFERENCES integration_connections(project_id, id),
    CHECK ((claim_token IS NULL) = (claim_expires_at IS NULL))
);
CREATE INDEX integration_connection_runtime_credential_idx
    ON integration_connection_runtime(credential_version_id);
CREATE INDEX integration_connections_active_provider_idx
    ON integration_connections(provider, id) WHERE state = 'active' AND deleted_at IS NULL;

-- A provider receipt is durable before acknowledgement. Its frozen plan tracks
-- independently committed recipient slots; provider I/O never runs in this
-- transaction. A random claim token fences retries after worker loss.
CREATE TABLE integration_inbox (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    project_id uuid NOT NULL,
    connection_id uuid NOT NULL,
    receipt_key text NOT NULL CHECK (octet_length(receipt_key) BETWEEN 1 AND 512),
    payload bytea NOT NULL CHECK (octet_length(payload) BETWEEN 1 AND 1048576),
    -- Only trusted app decisions populate normalized events; provider ingress leaves NULL.
    events jsonb CHECK (jsonb_typeof(events) = 'array' AND octet_length(events::text) <= 262144),
    plan jsonb CHECK (jsonb_typeof(plan) = 'object' AND octet_length(plan::text) <= 262144),
    progress jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(progress) = 'object' AND octet_length(progress::text) <= 262144),
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'processing', 'completed', 'failed', 'discarded')),
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
    CHECK ((state IN ('completed', 'discarded')) = (completed_at IS NOT NULL)),
    FOREIGN KEY (project_id, connection_id) REFERENCES integration_connections(project_id, id),
    UNIQUE (project_id, connection_id, receipt_key)
);

CREATE INDEX integration_inbox_ready_idx ON integration_inbox(available_at, id)
    WHERE state = 'pending';
CREATE INDEX integration_inbox_expired_idx ON integration_inbox(claim_expires_at, id)
    WHERE state = 'processing';
CREATE INDEX integration_inbox_terminal_idx ON integration_inbox(completed_at, id)
    WHERE state IN ('completed', 'discarded');
CREATE INDEX integration_inbox_connection_ready_idx
    ON integration_inbox(project_id, connection_id, available_at, id) WHERE state = 'pending';
CREATE INDEX integration_inbox_project_created_idx
    ON integration_inbox(project_id, created_at DESC, id DESC);
-- The first frozen plan reserves all launch slots for an app/conversation while
-- initial files are prepared outside a transaction. Failed plans retain this
-- evidence until explicit recovery; successful targets then retain membership.
CREATE INDEX integration_inbox_selection_idx ON integration_inbox USING gin
    ((jsonb_path_query_array(plan, '$.*.selection')) jsonb_path_ops)
    WHERE plan IS NOT NULL AND state IN ('pending', 'processing', 'failed');

-- Pre-launch menus belong to apps, never to agent execution. Source identity
-- survives menu expiry so a replay cannot revive the original request.
CREATE TABLE app_profile_choices (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    project_id uuid NOT NULL,
    connection_id uuid NOT NULL,
    app_id uuid NOT NULL,
    -- Publication belongs to the creating receipt and reuses its lease/recovery.
    -- Provenance only: receipt retention must not delete or constrain a choice.
    owner_receipt_id uuid NOT NULL,
    address_kind text NOT NULL CHECK (octet_length(address_kind) BETWEEN 1 AND 128),
    address_ref text NOT NULL CHECK (octet_length(address_ref) BETWEEN 1 AND 2048),
    source_key text NOT NULL CHECK (octet_length(source_key) BETWEEN 1 AND 512),
    event jsonb NOT NULL CHECK (jsonb_typeof(event) = 'object' AND octet_length(event::text) <= 262144),
    payload bytea NOT NULL CHECK (octet_length(payload) BETWEEN 1 AND 1048576),
    options jsonb NOT NULL CHECK (jsonb_typeof(options) = 'array'
        AND jsonb_array_length(options) BETWEEN 1 AND 16 AND octet_length(options::text) <= 16384),
    selected_key text CHECK (octet_length(selected_key) BETWEEN 1 AND 64),
    selected_by text CHECK (octet_length(selected_by) BETWEEN 1 AND 2048),
    message_channel_id text CHECK (octet_length(message_channel_id) BETWEEN 1 AND 2048),
    message_id text CHECK (octet_length(message_id) BETWEEN 1 AND 2048),
    expires_at timestamptz NOT NULL DEFAULT (statement_timestamp() + interval '1 hour'),
    created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    CHECK ((selected_key IS NULL) = (selected_by IS NULL)),
    CHECK ((message_channel_id IS NULL) = (message_id IS NULL)),
    CHECK (selected_key IS NULL OR message_id IS NOT NULL),
    FOREIGN KEY (project_id, connection_id) REFERENCES integration_connections(project_id, id),
    FOREIGN KEY (project_id, app_id) REFERENCES project_apps(project_id, id),
    UNIQUE (project_id, connection_id, app_id, source_key)
);
CREATE INDEX app_profile_choices_pending_idx
    ON app_profile_choices(project_id, connection_id, app_id, address_kind, address_ref, expires_at, id)
    WHERE selected_key IS NULL;
-- Conversation-scoped probes bridge selection to the first frozen inbox plan.
-- The joined inbox identity determines whether this retained choice is unsettled.
CREATE INDEX app_profile_choices_selected_conversation_idx
    ON app_profile_choices(project_id, connection_id, address_kind, address_ref, app_id, id)
    WHERE selected_key IS NOT NULL;
CREATE INDEX app_profile_choices_expiry_idx ON app_profile_choices(expires_at, id);

-- App resources follow the existing organization override mechanism.
ALTER TABLE org_resource_limit_overrides
    ADD COLUMN max_active_integration_connections_per_project bigint CHECK (max_active_integration_connections_per_project >= 0),
    ADD COLUMN max_active_project_apps_per_project bigint CHECK (max_active_project_apps_per_project >= 0),
    ADD COLUMN max_active_app_listeners_per_agent bigint CHECK (max_active_app_listeners_per_agent >= 0);

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
    1000::bigint AS max_active_integration_connections_per_project,
    1000::bigint AS max_active_project_apps_per_project,
    1024::bigint AS max_active_app_listeners_per_agent;

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
    coalesce(overrides.max_active_integration_connections_per_project, defaults.max_active_integration_connections_per_project) AS max_active_integration_connections_per_project,
    coalesce(overrides.max_active_project_apps_per_project, defaults.max_active_project_apps_per_project) AS max_active_project_apps_per_project,
    coalesce(overrides.max_active_app_listeners_per_agent, defaults.max_active_app_listeners_per_agent) AS max_active_app_listeners_per_agent
FROM orgs
CROSS JOIN default_resource_limits AS defaults
LEFT JOIN org_resource_limit_overrides AS overrides ON overrides.org_id = orgs.id
WHERE orgs.deleted_at IS NULL;

-- Interaction destinations capture immutable identity, not permission to use a
-- revoked connection. The dashboard interaction remains authoritative.
ALTER TABLE agents ADD COLUMN interaction_resource_key text
    CHECK (interaction_resource_key IS NULL OR (interaction_resource_key <> '' AND integration_target_id IS NOT NULL));
ALTER TABLE agent_interactions
    ADD COLUMN destination jsonb CHECK (destination IS NULL OR (jsonb_typeof(destination) = 'object' AND octet_length(destination::text) <= 4096)),
    ADD COLUMN presentation_receipt jsonb CHECK (presentation_receipt IS NULL OR (destination IS NOT NULL AND jsonb_typeof(presentation_receipt) = 'object' AND octet_length(presentation_receipt::text) <= 16384)),
    ADD COLUMN presentation_attempted_at timestamptz
        CHECK (presentation_attempted_at IS NULL OR destination IS NOT NULL);

-- Only unattempted captured interactions need discovery. The caller supplies
-- supported handler definitions; provider policy does not belong in this index.
CREATE INDEX agent_interactions_pending_presentation_idx
    ON agent_interactions ((destination ->> 'handler_definition'), created_at, agent_id, id)
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

    -- Reserve one best-effort attempt before provider I/O. This metadata-only
    -- transition cannot also resolve/cancel, replace the snapshot or save a receipt.
    IF OLD.state = 'open' AND OLD.destination IS NOT NULL
       AND OLD.presentation_attempted_at IS NULL AND NEW.presentation_attempted_at IS NOT NULL
       AND OLD.presentation_receipt IS NULL
       AND (to_jsonb(OLD) - 'presentation_attempted_at') = (to_jsonb(NEW) - 'presentation_attempted_at') THEN
        RETURN NEW;
    END IF;

    -- A late confirmed send may be recorded after cancellation so presentation
    -- cleanup can dismiss it. This exception cannot alter lifecycle or lineage.
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
