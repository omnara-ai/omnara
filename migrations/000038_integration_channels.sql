-- +goose Up

-- Existing channel rows are small enough to migrate atomically. The final
-- schema is built for future project shards; this transaction is sized to the
-- data that actually exists when it is introduced.
ALTER TABLE actors
    DROP CONSTRAINT actors_provider_check,
    ADD CONSTRAINT actors_provider_check
        CHECK (provider ~ '^[a-z0-9][a-z0-9_.-]{0,127}$');

ALTER TABLE actors
    DROP CONSTRAINT actors_check,
    ADD CONSTRAINT actors_check CHECK (
        provider NOT IN ('omnara', 'slack') OR provider_tenant_id IS NOT NULL
    ),
    ADD CONSTRAINT actors_channel_identity_bounds_check CHECK (
        (provider_tenant_id IS NULL OR octet_length(provider_tenant_id) <= 512)
        AND octet_length(provider_user_id) <= 512
        AND (display_name IS NULL OR octet_length(display_name) <= 1024)
        AND octet_length(metadata::text) <= 262144
    );

-- Connections share project ownership while retaining honest physical identity.
-- Profile ownership is converted to configured behavior before its columns are
-- removed below. Kind identifies authority, not provider behavior.
ALTER TABLE integration_installs RENAME COLUMN provider_agent_display_name TO display_name;
ALTER TABLE integration_installs RENAME COLUMN provider_metadata TO metadata;
ALTER TABLE integration_installs
    ALTER COLUMN provider DROP NOT NULL,
    ALTER COLUMN provider_tenant_id DROP NOT NULL,
    ALTER COLUMN provider_account_ref DROP NOT NULL;
UPDATE integration_installs SET integration_kind = 'managed';
DROP INDEX integration_installs_provider_tenant_account_idx;

ALTER TABLE integration_installs
    DROP CONSTRAINT integration_installs_provider_check,
    DROP CONSTRAINT integration_installs_provider_tenant_id_check,
    DROP CONSTRAINT integration_installs_check,
    ADD CONSTRAINT integration_installs_provider_check
        CHECK (provider ~ '^[a-z0-9][a-z0-9_.-]{0,127}$'),
    ADD CONSTRAINT integration_installs_tenant_shape_check
        CHECK (provider_tenant_id IS NULL OR provider_tenant_id <> ''),
    ADD CONSTRAINT integration_installs_channel_payload_bounds_check CHECK (
        octet_length(integration_kind) <= 128
        AND octet_length(connection_mode) <= 128
        AND octet_length(provider_tenant_id) <= 512
        AND octet_length(provider_account_ref) <= 512
        AND octet_length(display_name) <= 512
        AND octet_length(provider_config::text) <= 262144
        AND octet_length(provider_identity::text) <= 262144
        AND octet_length(metadata::text) <= 262144
    ),
    ADD COLUMN integration_app_id uuid,
    ADD COLUMN configuration_revision bigint NOT NULL DEFAULT 1,
    ADD CONSTRAINT integration_installs_configuration_revision_check
        CHECK (configuration_revision > 0),
    ADD CONSTRAINT integration_installs_project_id_id_app_key
        UNIQUE (project_id, id, integration_app_id);

-- Installation attribution is an account subject, not necessarily a human.
-- Existing user references remain unchanged; API keys use the same exclusive
-- principal shape as organization memberships and belong to this organization.
ALTER TABLE integration_installs
    ALTER COLUMN installed_by_user_id DROP NOT NULL,
    ADD COLUMN installed_by_org_api_key_id uuid,
    ADD CONSTRAINT integration_installs_installer_check
        CHECK (num_nonnulls(installed_by_user_id, installed_by_org_api_key_id) = 1),
    ADD CONSTRAINT integration_installs_installer_org_api_key_fkey
        FOREIGN KEY (org_id, installed_by_org_api_key_id)
        REFERENCES org_api_keys(org_id, id);

CREATE INDEX integration_installs_installer_org_api_key_idx
    ON integration_installs(org_id, installed_by_org_api_key_id)
    WHERE installed_by_org_api_key_id IS NOT NULL;

ALTER TABLE integration_targets
    ADD COLUMN parent_channel_id uuid,
    ADD CONSTRAINT integration_targets_channel_payload_bounds_check CHECK (
        octet_length(target_ref) <= 2048
        AND octet_length(provider_ref) <= 2048
        AND octet_length(provider_ref_kind) <= 128
        AND octet_length(display_name) <= 512
        AND octet_length(provider_metadata::text) <= 262144
    ),
    ADD CONSTRAINT integration_targets_project_id_id_key UNIQUE (project_id, id),
    ADD CONSTRAINT integration_targets_project_install_id_key
        UNIQUE (project_id, integration_install_id, id),
    ADD CONSTRAINT integration_targets_project_id_id_created_at_key
        UNIQUE (project_id, id, created_at);

-- The current destination is routing state, not ownership or an access grant.
ALTER TABLE agents
    DROP CONSTRAINT agents_project_id_id_integration_target_id_fkey,
    ADD CONSTRAINT agents_integration_target_fkey
        FOREIGN KEY (project_id, integration_target_id)
        REFERENCES integration_targets(project_id, id);

-- Capture the destination once when a prompt is created, including NULL for UI
-- only. Subsequent inputs and setter calls cannot relocate that prompt.
ALTER TABLE agent_interactions
    ADD COLUMN integration_target_id uuid REFERENCES integration_targets(id);

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
       interaction.integration_target_id
FROM agent_interactions interaction
JOIN tool_call_read_projection tool_call ON tool_call.agent_id = interaction.agent_id
  AND tool_call.id = interaction.tool_call_id;

-- Parentage records containment only; permissions always come from direct bindings.
ALTER TABLE integration_targets
    ADD CONSTRAINT integration_targets_parent_fkey
        FOREIGN KEY (project_id, integration_install_id, parent_channel_id)
        REFERENCES integration_targets(project_id, integration_install_id, id),
    ADD CONSTRAINT integration_targets_not_own_parent CHECK (parent_channel_id <> id);

ALTER TABLE agent_inputs
    ADD COLUMN integration_target_binding_id uuid;

ALTER TABLE secrets
    DROP CONSTRAINT secrets_kind_check,
    ADD CONSTRAINT secrets_kind_check CHECK (
        kind IN (
            'generic', 'oauth_token_set', 'slack_app_credentials',
            'aws_credentials', 'integration_credentials'
        )
    );

-- Provider applications are physical provider registrations: one Slack app,
-- Discord application, GitHub App, or equivalent. They are owned by an
-- organization and may optionally be restricted to one project. Provider
-- tenant/account installations remain project-owned.
CREATE TABLE integration_apps (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id uuid NOT NULL REFERENCES orgs(id),
    owner_project_id uuid,
    provider text NOT NULL,
    provider_app_ref text NOT NULL,
    display_name text NOT NULL DEFAULT '',
    connector_key text NOT NULL,
    credential_secret_id uuid,
    installation_credential_kind text,
    provider_config jsonb NOT NULL DEFAULT '{}'::jsonb,
    provider_metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    configuration_revision bigint NOT NULL DEFAULT 1,
    state text NOT NULL,
    deleted_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK (provider ~ '^[a-z0-9][a-z0-9_.-]{0,127}$'),
    CHECK (provider_app_ref <> ''),
    CHECK (octet_length(provider_app_ref) <= 512),
    CHECK (octet_length(display_name) <= 512),
    CHECK (connector_key ~ '^[a-z0-9][a-z0-9_.-]{0,127}$'),
    CHECK (installation_credential_kind IS NULL OR installation_credential_kind IN (
        'generic', 'oauth_token_set', 'slack_app_credentials',
        'aws_credentials', 'integration_credentials'
    )),
    CHECK (jsonb_typeof(provider_config) = 'object'),
    CHECK (jsonb_typeof(provider_metadata) = 'object'),
    CONSTRAINT integration_apps_provider_config_bytes_check
        CHECK (octet_length(provider_config::text) <= 262144),
    CONSTRAINT integration_apps_provider_metadata_bytes_check
        CHECK (octet_length(provider_metadata::text) <= 262144),
    CHECK (configuration_revision > 0),
    CHECK (state IN ('active', 'disabled')),
    FOREIGN KEY (org_id, owner_project_id) REFERENCES projects(org_id, id),
    FOREIGN KEY (org_id, credential_secret_id) REFERENCES secrets(org_id, id),
    UNIQUE (org_id, id)
);

CREATE UNIQUE INDEX integration_apps_org_provider_ref_idx
    ON integration_apps(org_id, provider, provider_app_ref)
    WHERE owner_project_id IS NULL AND deleted_at IS NULL;

CREATE UNIQUE INDEX integration_apps_project_provider_ref_idx
    ON integration_apps(owner_project_id, provider, provider_app_ref)
    WHERE owner_project_id IS NOT NULL AND deleted_at IS NULL;

CREATE INDEX integration_apps_credential_secret_idx
    ON integration_apps(org_id, credential_secret_id)
    WHERE credential_secret_id IS NOT NULL;

-- Secret deletion and credential association use one row-lock protocol. A
-- writer holds this shared lock while its referencing row becomes visible;
-- deletion locks the same secret before scanning references. These guards also
-- cover writes from an older API process during a rolling deployment.
-- +goose StatementBegin
CREATE FUNCTION lock_live_secret_reference()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    secret_id uuid;
BEGIN
    secret_id := (to_jsonb(NEW) ->> TG_ARGV[0])::uuid;
    IF TG_OP = 'UPDATE'
       AND NEW.org_id IS NOT DISTINCT FROM OLD.org_id
       AND secret_id IS NOT DISTINCT FROM (to_jsonb(OLD) ->> TG_ARGV[0])::uuid THEN
        RETURN NEW;
    END IF;
    IF secret_id IS NULL THEN
        RETURN NEW;
    END IF;

    PERFORM 1
    FROM secrets secret
    WHERE secret.org_id = NEW.org_id
      AND secret.id = secret_id
      AND secret.deleted_at IS NULL
    FOR SHARE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'credential secret must be active'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER model_provider_configs_credential_live
    BEFORE INSERT OR UPDATE OF org_id, credential_secret_id
    ON model_provider_configs
    FOR EACH ROW
    EXECUTE FUNCTION lock_live_secret_reference('credential_secret_id');

CREATE TRIGGER machine_pools_credential_live
    BEFORE INSERT OR UPDATE OF org_id, provider_auth_secret_id
    ON machine_pools
    FOR EACH ROW
    EXECUTE FUNCTION lock_live_secret_reference('provider_auth_secret_id');

CREATE TRIGGER integration_apps_credential_live
    BEFORE INSERT OR UPDATE OF org_id, credential_secret_id
    ON integration_apps
    FOR EACH ROW
    EXECUTE FUNCTION lock_live_secret_reference('credential_secret_id');

CREATE TRIGGER integration_installs_credential_live
    BEFORE INSERT OR UPDATE OF org_id, credential_secret_id
    ON integration_installs
    FOR EACH ROW
    EXECUTE FUNCTION lock_live_secret_reference('credential_secret_id');

-- Shared app credentials are organization-owned. A project-restricted app's
-- credential is owned by that exact project. The secret row is authoritative
-- for its payload kind, so the app does not duplicate it.
-- +goose StatementBegin
CREATE FUNCTION integration_app_validate_credential_scope()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.credential_secret_id IS NULL THEN
        RETURN NEW;
    END IF;

    PERFORM 1
    FROM secrets secret
    WHERE secret.org_id = NEW.org_id
      AND secret.id = NEW.credential_secret_id
      AND secret.management_kind = 'tenant'
      AND secret.deleted_at IS NULL
      AND (
        (NEW.owner_project_id IS NULL AND secret.owner_kind = 'org')
        OR
        (NEW.owner_project_id IS NOT NULL
          AND secret.owner_kind = 'project'
          AND secret.owner_project_id = NEW.owner_project_id)
      );
    IF NOT FOUND THEN
        RAISE EXCEPTION 'integration app credential is outside the app owner scope'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER integration_apps_validate_credential_scope
    BEFORE INSERT OR UPDATE OF org_id, owner_project_id, credential_secret_id
    ON integration_apps
    FOR EACH ROW
    EXECUTE FUNCTION integration_app_validate_credential_scope();

-- Configuration revisions are the gateway cache fence. Direct app mutations
-- cannot forget to advance it.
-- +goose StatementBegin
CREATE FUNCTION integration_app_advance_configuration_revision()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.display_name IS DISTINCT FROM NEW.display_name
       OR OLD.credential_secret_id IS DISTINCT FROM NEW.credential_secret_id
       OR OLD.provider_config IS DISTINCT FROM NEW.provider_config
       OR OLD.provider_metadata IS DISTINCT FROM NEW.provider_metadata
       OR OLD.state IS DISTINCT FROM NEW.state
       OR OLD.deleted_at IS DISTINCT FROM NEW.deleted_at THEN
        NEW.configuration_revision := OLD.configuration_revision + 1;
        NEW.updated_at := statement_timestamp();
    ELSIF NEW.configuration_revision < OLD.configuration_revision THEN
        RAISE EXCEPTION 'integration app configuration revision cannot decrease'
            USING ERRCODE = '25006';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER integration_apps_advance_configuration_revision
    BEFORE UPDATE ON integration_apps
    FOR EACH ROW
    EXECUTE FUNCTION integration_app_advance_configuration_revision();

-- Identity columns across the integration schema are write-once provenance.
-- Column-specific triggers keep normal lifecycle updates off this function.
-- +goose StatementBegin
CREATE FUNCTION reject_immutable_column_update()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION '% identity columns are immutable', TG_TABLE_NAME
        USING ERRCODE = '25006';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER integration_apps_identity_immutable
    BEFORE UPDATE OF id, org_id, owner_project_id, provider, provider_app_ref,
        connector_key, installation_credential_kind, created_at
    ON integration_apps
    FOR EACH ROW
    EXECUTE FUNCTION reject_immutable_column_update();

CREATE TRIGGER agent_interactions_destination_immutable
    BEFORE UPDATE OF integration_target_id ON agent_interactions
    FOR EACH ROW
    WHEN (OLD.integration_target_id IS DISTINCT FROM NEW.integration_target_id)
    EXECUTE FUNCTION reject_immutable_column_update();

-- Old binaries already retire installations, but know nothing about app
-- registrations. This trigger fences project-owned compatibility apps when an
-- old binary deletes their project. Current lifecycle code also performs the
-- same update explicitly. Organization deletion reaches these apps by deleting
-- its projects; shared apps only exist on binaries that delete them explicitly.
-- +goose StatementBegin
CREATE FUNCTION integration_project_retire_apps_on_delete()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.deleted_at IS NULL AND NEW.deleted_at IS NOT NULL THEN
        UPDATE integration_apps app
        SET credential_secret_id = NULL,
            state = 'disabled',
            deleted_at = NEW.deleted_at,
            updated_at = statement_timestamp()
        WHERE app.org_id = NEW.org_id
          AND app.owner_project_id = NEW.id
          AND app.deleted_at IS NULL;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER projects_retire_integration_apps
    AFTER UPDATE OF deleted_at ON projects
    FOR EACH ROW
    EXECUTE FUNCTION integration_project_retire_apps_on_delete();

-- An organization-shared app may be installed by any project in its
-- organization; a restricted app may only be installed by its owner project.
-- +goose StatementBegin
CREATE FUNCTION integration_install_validate_app_scope()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    app integration_apps%ROWTYPE;
BEGIN
    IF NEW.integration_kind = 'external' THEN
        RETURN NEW; -- The connection-shape CHECK prohibits all managed fields.
    END IF;
    SELECT candidate.* INTO app
    FROM integration_apps candidate
    WHERE candidate.org_id = NEW.org_id
      AND candidate.id = NEW.integration_app_id
    FOR SHARE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'integration app does not exist in the installation organization'
            USING ERRCODE = '23503';
    END IF;
    IF app.provider <> NEW.provider
       OR (app.owner_project_id IS NOT NULL AND app.owner_project_id <> NEW.project_id) THEN
        RAISE EXCEPTION 'integration app is outside the installation scope'
            USING ERRCODE = '23514';
    END IF;
    IF NEW.deleted_at IS NULL AND app.deleted_at IS NOT NULL THEN
        RAISE EXCEPTION 'installation requires an undeleted integration app'
            USING ERRCODE = '23514';
    END IF;
    IF NEW.state = 'active' AND NEW.deleted_at IS NULL
       AND app.state <> 'active' THEN
        RAISE EXCEPTION 'active installation requires an active integration app'
            USING ERRCODE = '23514';
    END IF;
    IF app.installation_credential_kind IS NULL THEN
        IF NEW.credential_secret_id IS NOT NULL THEN
            RAISE EXCEPTION 'integration app does not accept installation credentials'
                USING ERRCODE = '23514';
        END IF;
    ELSIF NEW.state = 'active' AND NEW.deleted_at IS NULL THEN
        PERFORM 1
        FROM secrets secret
        WHERE secret.org_id = NEW.org_id
          AND secret.id = NEW.credential_secret_id
          AND secret.management_kind = 'tenant'
          AND secret.owner_kind = 'project'
          AND secret.owner_project_id = NEW.project_id
          AND secret.kind = app.installation_credential_kind
          AND secret.deleted_at IS NULL;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'integration installation credential does not match the app contract'
                USING ERRCODE = '23514';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- A route is one configured inbound behavior implementation. It never owns an
-- agent; the optional profile authorizes launches by this configured behavior.
CREATE TABLE integration_routes (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    project_id uuid NOT NULL,
    integration_install_id uuid NOT NULL,
    deployment_key text NOT NULL,
    behavior_key text NOT NULL,
    configuration jsonb NOT NULL DEFAULT '{}'::jsonb,
    agent_profile_id uuid,
    state text NOT NULL,
    deleted_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK (deployment_key <> '' AND octet_length(deployment_key) <= 512),
    CHECK (behavior_key ~ '^[a-z0-9][a-z0-9_.-]{0,127}$'),
    CHECK (jsonb_typeof(configuration) = 'object'),
    CONSTRAINT integration_routes_configuration_bytes_check
        CHECK (octet_length(configuration::text) <= 262144),
    CHECK (state IN ('active', 'disabled')),
    FOREIGN KEY (project_id, integration_install_id) REFERENCES integration_installs(project_id, id),
    FOREIGN KEY (project_id, agent_profile_id) REFERENCES agent_profiles(project_id, id),
    UNIQUE (project_id, integration_install_id, id),
    UNIQUE (project_id, integration_install_id, deployment_key)
);

CREATE INDEX integration_routes_active_install_idx
    ON integration_routes(project_id, integration_install_id, created_at, id)
    WHERE state = 'active' AND deleted_at IS NULL;

CREATE INDEX integration_routes_profile_idx
    ON integration_routes(project_id, agent_profile_id)
    WHERE agent_profile_id IS NOT NULL;

CREATE TRIGGER integration_routes_definition_immutable
    BEFORE UPDATE OF id, project_id, integration_install_id, deployment_key,
        behavior_key, configuration, agent_profile_id, created_at
    ON integration_routes
    FOR EACH ROW
    EXECUTE FUNCTION reject_immutable_column_update();

-- Behavior instance identity is distinct from an incoming event. A PR or chat
-- thread keeps its agent across new events, gateway restarts and Redis expiry.
CREATE TABLE integration_workflows (
    project_id uuid NOT NULL,
    integration_install_id uuid NOT NULL,
    integration_route_id uuid NOT NULL,
    instance_key text NOT NULL CHECK (instance_key <> '' AND octet_length(instance_key) <= 512),
    agent_id uuid NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (project_id, integration_install_id, integration_route_id, instance_key),
    FOREIGN KEY (project_id, integration_install_id, integration_route_id)
        REFERENCES integration_routes(project_id, integration_install_id, id),
    FOREIGN KEY (project_id, agent_id) REFERENCES agents(project_id, id)
);

CREATE TRIGGER integration_workflows_identity_immutable
    BEFORE UPDATE ON integration_workflows
    FOR EACH ROW
    EXECUTE FUNCTION reject_immutable_column_update();

-- Installation revisions fence its lazily loaded configuration without forcing
-- every installation of the same app to reload or restart.
-- +goose StatementBegin
CREATE FUNCTION integration_install_advance_configuration_revision()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.connection_mode IS DISTINCT FROM NEW.connection_mode
       OR OLD.state IS DISTINCT FROM NEW.state
       OR OLD.display_name IS DISTINCT FROM NEW.display_name
       OR OLD.credential_secret_id IS DISTINCT FROM NEW.credential_secret_id
       OR OLD.provider_config IS DISTINCT FROM NEW.provider_config
       OR OLD.provider_identity IS DISTINCT FROM NEW.provider_identity
       OR OLD.metadata IS DISTINCT FROM NEW.metadata
       OR OLD.deleted_at IS DISTINCT FROM NEW.deleted_at THEN
        NEW.configuration_revision := OLD.configuration_revision + 1;
        NEW.updated_at := statement_timestamp();
    ELSIF NEW.configuration_revision < OLD.configuration_revision THEN
        RAISE EXCEPTION 'integration installation configuration revision cannot decrease'
            USING ERRCODE = '25006';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- Bindings authorize one agent to receive from and/or send to an external address.
-- Route provenance is retained so independently configured behaviors can be revoked safely.
CREATE TABLE integration_target_bindings (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    project_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    integration_install_id uuid NOT NULL,
    integration_target_id uuid NOT NULL,
    target_created_at timestamptz NOT NULL,
    integration_route_id uuid,
    receive_allowed boolean NOT NULL,
    read_allowed boolean NOT NULL DEFAULT false,
    send_allowed boolean NOT NULL,
    reply_receive_allowed boolean,
    reply_read_allowed boolean,
    reply_send_allowed boolean,
    source text NOT NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK (receive_allowed OR read_allowed OR send_allowed),
    CONSTRAINT integration_target_bindings_reply_grants_check CHECK (
        num_nonnulls(reply_receive_allowed, reply_read_allowed, reply_send_allowed) IN (0, 3)
        AND (reply_receive_allowed IS NULL OR (
            send_allowed AND (reply_receive_allowed OR reply_read_allowed OR reply_send_allowed)
        ))
    ),
    CHECK (source <> '' AND octet_length(source) <= 128),
    CHECK (jsonb_typeof(metadata) = 'object'),
    CONSTRAINT integration_target_bindings_metadata_bytes_check
        CHECK (octet_length(metadata::text) <= 262144),
    FOREIGN KEY (project_id, agent_id) REFERENCES agents(project_id, id),
    FOREIGN KEY (project_id, integration_install_id, integration_target_id)
        REFERENCES integration_targets(project_id, integration_install_id, id),
    FOREIGN KEY (project_id, integration_target_id, target_created_at)
        REFERENCES integration_targets(project_id, id, created_at),
    FOREIGN KEY (project_id, integration_install_id, integration_route_id)
        REFERENCES integration_routes(project_id, integration_install_id, id),
    UNIQUE (project_id, agent_id, integration_target_id, id)
);

-- Revocation is the binding's only lifecycle transition and is irreversible.
-- +goose StatementBegin
CREATE FUNCTION integration_target_binding_reject_revocation_change()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.revoked_at IS NOT NULL
       AND OLD.revoked_at IS DISTINCT FROM NEW.revoked_at THEN
        RAISE EXCEPTION 'integration target binding revocation is immutable'
            USING ERRCODE = '25006';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER integration_target_bindings_definition_immutable
    BEFORE UPDATE OF id, project_id, agent_id, integration_install_id,
        integration_target_id, target_created_at, integration_route_id,
        receive_allowed, read_allowed, send_allowed,
        reply_receive_allowed, reply_read_allowed, reply_send_allowed, source, metadata, created_at
    ON integration_target_bindings
    FOR EACH ROW
    EXECUTE FUNCTION reject_immutable_column_update();

CREATE TRIGGER integration_target_bindings_revocation_immutable
    BEFORE UPDATE OF revoked_at ON integration_target_bindings
    FOR EACH ROW
    EXECUTE FUNCTION integration_target_binding_reject_revocation_change();

CREATE UNIQUE INDEX integration_target_bindings_active_route_idx
    ON integration_target_bindings(
        project_id, agent_id, integration_target_id, integration_route_id
    )
    WHERE integration_route_id IS NOT NULL AND revoked_at IS NULL;

CREATE UNIQUE INDEX integration_target_bindings_active_routeless_source_idx
    ON integration_target_bindings(
        project_id, agent_id, integration_target_id, source
    )
    WHERE integration_route_id IS NULL AND revoked_at IS NULL;

CREATE INDEX integration_target_bindings_agent_send_idx
    ON integration_target_bindings(project_id, agent_id, integration_target_id, id)
    WHERE send_allowed AND revoked_at IS NULL;

CREATE INDEX integration_target_bindings_operation_order_idx
    ON integration_target_bindings(project_id, agent_id, integration_target_id, created_at, id)
    WHERE revoked_at IS NULL;

CREATE INDEX integration_target_bindings_agent_target_order_idx
    ON integration_target_bindings(
        project_id, agent_id, target_created_at DESC, integration_target_id DESC
    )
    WHERE revoked_at IS NULL;

CREATE INDEX integration_target_bindings_target_receive_idx
    ON integration_target_bindings(project_id, integration_target_id, integration_route_id, id)
    WHERE receive_allowed AND revoked_at IS NULL;

-- History includes revoked bindings; live delivery pages deduplicate by agent.
CREATE INDEX integration_target_bindings_target_history_idx
    ON integration_target_bindings(project_id, integration_install_id, integration_target_id);

CREATE INDEX integration_target_bindings_target_recipients_idx
    ON integration_target_bindings(project_id, integration_install_id, integration_target_id, agent_id, id)
    WHERE receive_allowed AND revoked_at IS NULL;

CREATE INDEX integration_target_bindings_install_idx
    ON integration_target_bindings(project_id, integration_install_id, id)
    WHERE revoked_at IS NULL;

-- Historical inputs keep NULL binding provenance. Every new channel input must
-- supply its explicit binding; no trigger guesses authority from an old owner.
-- +goose StatementBegin
CREATE FUNCTION agent_input_require_integration_binding()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.integration_target_id IS NOT NULL AND NEW.integration_target_binding_id IS NULL THEN
        RAISE EXCEPTION 'target-backed agent input requires an explicit binding'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER agent_inputs_integration_binding_immutable
    BEFORE UPDATE OF integration_target_binding_id ON agent_inputs
    FOR EACH ROW
    WHEN (OLD.integration_target_binding_id IS DISTINCT FROM NEW.integration_target_binding_id)
    EXECUTE FUNCTION reject_immutable_column_update();

-- A connection publishes each current address contract once; its destinations
-- reference it. Schema changes do not rewrite every thread or create versions.
CREATE TABLE integration_channel_definitions (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    project_id uuid NOT NULL,
    integration_install_id uuid NOT NULL,
    implementation_key text NOT NULL CHECK (implementation_key ~ '^[a-z0-9][a-z0-9_.-]{0,127}$'),
    kind text NOT NULL CHECK (kind IN ('SLACK_CHANNEL', 'SLACK_THREAD', 'EXTERNAL')),
    description text NOT NULL CHECK (octet_length(description) <= 16384),
    send_params_schema jsonb NOT NULL CHECK (
        jsonb_typeof(send_params_schema) = 'object'
        AND octet_length(send_params_schema::text) <= 262144
    ),
    capabilities jsonb NOT NULL CHECK (
        jsonb_typeof(capabilities) = 'object'
        AND octet_length(capabilities::text) <= 4096
    ),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    FOREIGN KEY (project_id, integration_install_id) REFERENCES integration_installs(project_id, id),
    UNIQUE (project_id, integration_install_id, id),
    UNIQUE (project_id, integration_install_id, implementation_key)
);

CREATE TRIGGER integration_channel_definitions_identity_immutable
    BEFORE UPDATE OF id, project_id, integration_install_id, implementation_key, kind, created_at
    ON integration_channel_definitions
    FOR EACH ROW
    EXECUTE FUNCTION reject_immutable_column_update();

ALTER TABLE integration_targets
    ADD COLUMN channel_definition_id uuid,
    ADD CONSTRAINT integration_targets_definition_fkey
        FOREIGN KEY (project_id, integration_install_id, channel_definition_id)
        REFERENCES integration_channel_definitions(project_id, integration_install_id, id);

-- Verified incoming events are persisted before acknowledging their provider.
-- Processing is replayable; completion is separate from the recipient's atomic
-- agent/input transaction so neither operation requires a cross-shard commit.
CREATE TABLE integration_event_receipts (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    project_id uuid NOT NULL,
    integration_install_id uuid NOT NULL,
    integration_app_id uuid NOT NULL,
    connector_key text NOT NULL,
    provider text NOT NULL,
    event_id text NOT NULL CHECK (event_id <> '' AND octet_length(event_id) <= 512),
    payload jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
    state text NOT NULL CHECK (state IN ('pending', 'processing', 'completed', 'failed')),
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    available_at timestamptz NOT NULL,
    lease_token uuid,
    lease_generation bigint NOT NULL DEFAULT 0 CHECK (lease_generation >= 0),
    lease_expires_at timestamptz,
    last_error jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(last_error) = 'object'),
    completed_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT integration_event_receipts_payload_bytes_check
        CHECK (octet_length(payload::text) <= 25165824),
    CONSTRAINT integration_event_receipts_last_error_bytes_check
        CHECK (octet_length(last_error::text) <= 262144),
    CHECK (
        (state = 'processing' AND lease_token IS NOT NULL AND lease_expires_at IS NOT NULL)
        OR (state <> 'processing' AND lease_token IS NULL AND lease_expires_at IS NULL)
    ),
    CHECK ((state IN ('completed', 'failed')) = (completed_at IS NOT NULL)),
    FOREIGN KEY (project_id, integration_install_id, integration_app_id)
        REFERENCES integration_installs(project_id, id, integration_app_id),
    UNIQUE (project_id, integration_install_id, event_id)
);

CREATE INDEX integration_event_receipts_due_idx
    ON integration_event_receipts(connector_key, provider, available_at, id)
    WHERE state IN ('pending', 'processing');

-- Bound each maintenance candidate page before current-owner checks.
CREATE INDEX integration_event_receipts_maintenance_idx
    ON integration_event_receipts(id)
    WHERE state IN ('pending', 'processing');

CREATE INDEX integration_event_receipts_terminal_retention_idx
    ON integration_event_receipts(completed_at, id)
    WHERE state IN ('completed', 'failed');

CREATE TRIGGER integration_event_receipts_identity_immutable
    BEFORE UPDATE OF id, project_id, integration_install_id, integration_app_id,
        connector_key, provider, event_id, payload, created_at
    ON integration_event_receipts
    FOR EACH ROW
    EXECUTE FUNCTION reject_immutable_column_update();

-- Shard-local maintenance advances durable cursors through large integration
-- tables. Cursor IDs deliberately are not foreign keys because lifecycle and
-- retention may delete the row most recently examined. A fixed cycle end keeps
-- sustained tail inserts from starving older rows.
CREATE TABLE integration_sweep_cursors (
    sweep_kind text PRIMARY KEY,
    last_item_id uuid NOT NULL,
    cycle_end_id uuid,
    updated_at timestamptz NOT NULL,
    CHECK (sweep_kind <> '')
);

INSERT INTO integration_sweep_cursors (
    sweep_kind, last_item_id, cycle_end_id, updated_at
) VALUES (
    'event_unprocessable', '00000000-0000-0000-0000-000000000000', NULL, transaction_timestamp()
);

-- Persistent transports lease opaque runtime units. Provider-specific checkpoint meaning
-- stays in the adapter; token plus generation fence every stale owner operation.
CREATE TABLE integration_runtime_units (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id uuid NOT NULL,
    integration_app_id uuid NOT NULL,
    project_id uuid,
    integration_install_id uuid,
    provider text NOT NULL,
    connector_key text NOT NULL,
    unit_key text NOT NULL,
    runtime_kind text NOT NULL,
    desired_state text NOT NULL,
    spec_revision integer NOT NULL,
    configuration jsonb NOT NULL DEFAULT '{}'::jsonb,
    status text NOT NULL,
    failure_count integer NOT NULL DEFAULT 0,
    available_at timestamptz NOT NULL,
    lease_owner text,
    lease_token uuid,
    lease_generation bigint NOT NULL DEFAULT 0,
    leased_at timestamptz,
    renewed_at timestamptz,
    lease_expires_at timestamptz,
    lease_spec_revision integer,
    lease_app_configuration_revision bigint,
    lease_install_configuration_revision bigint,
    checkpoint_version integer NOT NULL DEFAULT 1,
    checkpoint_revision bigint NOT NULL DEFAULT 0,
    checkpoint jsonb NOT NULL DEFAULT '{}'::jsonb,
    last_error jsonb NOT NULL DEFAULT '{}'::jsonb,
    deleted_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK (unit_key <> '' AND octet_length(unit_key) <= 512),
    CHECK (runtime_kind ~ '^[a-z0-9][a-z0-9_.-]{0,127}$'),
    CHECK (provider ~ '^[a-z0-9][a-z0-9_.-]{0,127}$'),
    CHECK (connector_key ~ '^[a-z0-9][a-z0-9_.-]{0,127}$'),
    CHECK (desired_state IN ('running', 'stopped')),
    CHECK (spec_revision > 0),
    CHECK (jsonb_typeof(configuration) = 'object'),
    CONSTRAINT integration_runtime_units_configuration_bytes_check
        CHECK (octet_length(configuration::text) <= 262144),
    CHECK (status IN ('idle', 'running', 'error', 'stopped')),
    CHECK (failure_count >= 0),
    CHECK (lease_generation >= 0),
    CHECK (checkpoint_version > 0),
    CHECK (checkpoint_revision >= 0),
    CHECK (jsonb_typeof(checkpoint) = 'object'),
    CHECK (jsonb_typeof(last_error) = 'object'),
    CONSTRAINT integration_runtime_units_checkpoint_bytes_check
        CHECK (octet_length(checkpoint::text) <= 262144),
    CONSTRAINT integration_runtime_units_last_error_bytes_check
        CHECK (octet_length(last_error::text) <= 262144),
    CHECK (lease_owner IS NULL OR octet_length(lease_owner) <= 256),
    CHECK ((project_id IS NULL) = (integration_install_id IS NULL)),
    CHECK (
        (lease_token IS NULL AND lease_owner IS NULL AND leased_at IS NULL
          AND renewed_at IS NULL AND lease_expires_at IS NULL
          AND lease_spec_revision IS NULL
          AND lease_app_configuration_revision IS NULL
          AND lease_install_configuration_revision IS NULL)
        OR
        (lease_token IS NOT NULL AND lease_owner IS NOT NULL AND leased_at IS NOT NULL
          AND renewed_at IS NOT NULL AND lease_expires_at IS NOT NULL
          AND lease_spec_revision IS NOT NULL
          AND lease_app_configuration_revision IS NOT NULL
          AND ((integration_install_id IS NULL) = (lease_install_configuration_revision IS NULL)))
    ),
    CHECK (lease_expires_at IS NULL OR lease_expires_at > renewed_at),
    FOREIGN KEY (org_id, integration_app_id) REFERENCES integration_apps(org_id, id),
    FOREIGN KEY (org_id, project_id) REFERENCES projects(org_id, id),
    FOREIGN KEY (project_id, integration_install_id, integration_app_id)
        REFERENCES integration_installs(project_id, id, integration_app_id)
);

-- A deleted runtime is a historical fenced lease lineage, not the active
-- provider unit. Reinstallation may therefore create a fresh row with the same
-- provider key without inheriting its predecessor's token or checkpoint.
CREATE UNIQUE INDEX integration_runtime_units_active_app_key_idx
    ON integration_runtime_units(integration_app_id, unit_key)
    WHERE project_id IS NULL AND deleted_at IS NULL;

CREATE UNIQUE INDEX integration_runtime_units_active_install_key_idx
    ON integration_runtime_units(integration_app_id, integration_install_id, unit_key)
    WHERE project_id IS NOT NULL AND deleted_at IS NULL;

CREATE INDEX integration_runtime_units_claim_idx
    ON integration_runtime_units(connector_key, provider, available_at, id)
    WHERE desired_state = 'running' AND deleted_at IS NULL;

CREATE TRIGGER integration_runtime_units_identity_immutable
    BEFORE UPDATE OF id, org_id, integration_app_id, project_id,
        integration_install_id, provider, connector_key, unit_key,
        runtime_kind, created_at
    ON integration_runtime_units
    FOR EACH ROW
    EXECUTE FUNCTION reject_immutable_column_update();

-- Credential rotation advances only the configuration boundary that owns the
-- secret. This avoids O(all installations) app cache invalidation.
-- +goose StatementBegin
CREATE FUNCTION integration_secret_touch_configuration_revisions()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    -- A successful deletion cannot have credential dependents, and a
    -- referenced deletion rolls back. Avoid taking dependent-row locks while
    -- the deletion protocol holds the secret row exclusively.
    IF NEW.deleted_at IS NOT NULL THEN
        RETURN NEW;
    END IF;
    IF OLD.current_version_id IS NOT DISTINCT FROM NEW.current_version_id THEN
        RETURN NEW;
    END IF;
    -- Credential rotation follows the same installation→application lock order
    -- as target/runtime authority and lifecycle deletion.
    UPDATE integration_installs install
    SET configuration_revision = install.configuration_revision + 1,
        updated_at = statement_timestamp()
    WHERE install.org_id = NEW.org_id
      AND install.credential_secret_id = NEW.id
      AND install.deleted_at IS NULL;

    UPDATE integration_apps app
    SET configuration_revision = app.configuration_revision + 1,
        updated_at = statement_timestamp()
    WHERE app.org_id = NEW.org_id
      AND app.deleted_at IS NULL
      AND app.credential_secret_id = NEW.id;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- The observed deployed shape is profile-backed Slack. Do not guess a profile
-- for fixed-agent installs or silently discard an unsupported conversation.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM integration_installs WHERE provider <> 'slack' OR agent_profile_id IS NULL) THEN
        RAISE EXCEPTION 'channel cutover requires profile-backed Slack installations; inspect unsupported ownership';
    END IF;
    IF EXISTS (
        SELECT 1 FROM integration_installs install
        JOIN projects project ON project.id = install.project_id
        JOIN orgs organization ON organization.id = install.org_id
        JOIN agent_profiles profile ON profile.project_id = install.project_id AND profile.id = install.agent_profile_id
        JOIN agent_profile_versions version ON version.project_id = profile.project_id AND version.id = profile.current_version_id
        WHERE install.state = 'active' AND install.deleted_at IS NULL
          AND project.deleted_at IS NULL AND organization.deleted_at IS NULL
          AND (profile.deleted_at IS NOT NULL OR version.deleted_at IS NOT NULL)
    ) THEN
        RAISE EXCEPTION 'channel cutover requires live profiles for active Slack installations';
    END IF;
    IF EXISTS (
        SELECT 1 FROM integration_targets
        WHERE provider_ref_kind NOT IN ('dm', 'thread') OR octet_length(provider_ref) > 512
    ) THEN
        RAISE EXCEPTION 'channel cutover encountered an unsupported Slack conversation address';
    END IF;
END;
$$;
-- +goose StatementEnd

-- Existing OAuth provider_account_ref is Slack's real api_app_id. Keep the
-- combined installation credential and project ownership; no app secret is made.
INSERT INTO integration_apps(
    org_id, owner_project_id, provider, provider_app_ref, display_name,
    connector_key, installation_credential_kind, state, deleted_at,
    created_at, updated_at
)
SELECT install.org_id,
       install.project_id,
       install.provider,
       install.provider_account_ref,
       max(install.display_name),
       'chat_sdk',
       'slack_app_credentials',
       CASE
         WHEN project.deleted_at IS NULL AND organization.deleted_at IS NULL
           THEN 'active'
         ELSE 'disabled'
       END,
       coalesce(project.deleted_at, organization.deleted_at),
       min(install.created_at),
       max(install.updated_at)
FROM integration_installs install
JOIN projects project
  ON project.org_id = install.org_id
 AND project.id = install.project_id
JOIN orgs organization ON organization.id = install.org_id
GROUP BY install.org_id, install.project_id, install.provider,
         install.provider_account_ref, project.deleted_at, organization.deleted_at;

UPDATE integration_installs install
SET integration_app_id = app.id
FROM integration_apps app
WHERE app.org_id = install.org_id
  AND app.owner_project_id = install.project_id
  AND app.provider = install.provider
  AND app.provider_app_ref = install.provider_account_ref;

ALTER TABLE integration_installs
    ADD CONSTRAINT integration_installs_app_fkey
        FOREIGN KEY (org_id, integration_app_id)
        REFERENCES integration_apps(org_id, id);

ALTER TABLE integration_installs
    ADD CONSTRAINT integration_installs_kind_check CHECK (integration_kind IN ('managed', 'external')),
    ADD CONSTRAINT integration_installs_connection_shape_check CHECK (
        (integration_kind = 'managed'
         AND integration_app_id IS NOT NULL AND provider IS NOT NULL AND provider_account_ref IS NOT NULL
         AND (provider <> 'slack' OR provider_tenant_id IS NOT NULL))
        OR (integration_kind = 'external' AND connection_mode = 'api'
         AND integration_app_id IS NULL AND provider IS NULL
         AND provider_account_ref IS NULL AND provider_tenant_id IS NULL
         AND credential_secret_id IS NULL AND last_oauth_flow_id IS NULL
         AND provider_config = '{}'::jsonb AND provider_identity = '{}'::jsonb)
    );

UPDATE integration_installs SET provider_tenant_id = NULL WHERE provider_tenant_id = '';
CREATE UNIQUE INDEX integration_installs_app_tenant_account_idx
    ON integration_installs(integration_app_id, provider_tenant_id, provider_account_ref) NULLS NOT DISTINCT
    WHERE integration_kind = 'managed' AND deleted_at IS NULL;

-- Preserve the released Slack callback identity across project-owned logical
-- app registrations. Other providers keep their app-scoped installation keys.
CREATE UNIQUE INDEX integration_installs_slack_tenant_account_idx
    ON integration_installs(provider_tenant_id, provider_account_ref)
    WHERE integration_kind = 'managed' AND provider = 'slack' AND deleted_at IS NULL;

-- Translate installation setup into the same route created by managed OAuth.
INSERT INTO integration_routes (
    project_id, integration_install_id, deployment_key, behavior_key,
    configuration, agent_profile_id, state, deleted_at, created_at, updated_at
)
SELECT install.project_id, install.id, 'slack', 'slack_conversation',
       '{}'::jsonb, install.agent_profile_id,
       CASE WHEN app.deleted_at IS NULL THEN install.state ELSE 'disabled' END,
       coalesce(install.deleted_at, app.deleted_at), install.created_at, install.updated_at
FROM integration_installs install
JOIN integration_apps app ON app.org_id = install.org_id AND app.id = install.integration_app_id;

-- These are the current Slack definitions published by the gateway. Provider
-- capabilities do not grant agents read access or continuation delegation.
INSERT INTO integration_channel_definitions (
    project_id, integration_install_id, implementation_key, kind, description,
    send_params_schema, capabilities, created_at, updated_at
)
SELECT install.project_id, install.id, definition.implementation_key, definition.kind, definition.description,
       '{"type":"object","properties":{},"additionalProperties":false}'::jsonb,
       jsonb_build_object('read', true, 'send', true, 'text', true, 'artifacts', true,
           'permissions', true, 'questions', true, 'creates_reply_channel', definition.kind = 'SLACK_CHANNEL'),
       install.created_at, install.updated_at
FROM integration_installs install
CROSS JOIN (VALUES
    ('slack_channel', 'SLACK_CHANNEL', 'A Slack conversation.'),
    ('slack_thread', 'SLACK_THREAD', 'A Slack message thread.')
) AS definition(implementation_key, kind, description);

UPDATE integration_targets target
SET channel_definition_id = definition.id
FROM integration_channel_definitions definition
WHERE definition.project_id = target.project_id AND definition.integration_install_id = target.integration_install_id
  AND definition.implementation_key = CASE target.provider_ref_kind WHEN 'dm' THEN 'slack_channel' ELSE 'slack_thread' END;

-- Preserve every live conversation's exact agent, including archived agents.
-- Deleted addresses remain historical targets and revoked bindings, not a live
-- behavior association that would collide with a subsequently recreated address.
INSERT INTO integration_workflows (
    project_id, integration_install_id, integration_route_id, instance_key, agent_id, created_at
)
SELECT target.project_id, target.integration_install_id, route.id, target.provider_ref, target.agent_id, target.created_at
FROM integration_targets target
JOIN integration_routes route ON route.project_id = target.project_id AND route.integration_install_id = target.integration_install_id
WHERE target.deleted_at IS NULL;

INSERT INTO integration_target_bindings (
    project_id, agent_id, integration_install_id, integration_target_id,
    target_created_at, integration_route_id, receive_allowed, read_allowed, send_allowed,
    source, revoked_at, created_at, updated_at
)
SELECT target.project_id, target.agent_id, target.integration_install_id, target.id,
       target.created_at, route.id, true, false, true, 'channel',
       coalesce(target.deleted_at, route.deleted_at), target.created_at,
       greatest(target.updated_at, route.updated_at)
FROM integration_targets target
JOIN integration_routes route ON route.project_id = target.project_id AND route.integration_install_id = target.integration_install_id;

-- Historical inputs intentionally retain their original target and NULL binding.
ALTER TABLE agent_inputs
    DROP CONSTRAINT agent_inputs_project_id_agent_id_integration_target_id_fkey,
    ADD CONSTRAINT agent_inputs_integration_target_fkey
        FOREIGN KEY (project_id, integration_target_id)
        REFERENCES integration_targets(project_id, id),
    ADD CONSTRAINT agent_inputs_integration_binding_fkey
        FOREIGN KEY (
            project_id, agent_id, integration_target_id,
            integration_target_binding_id
        ) REFERENCES integration_target_bindings(
            project_id, agent_id, integration_target_id, id
        ),
    ADD CONSTRAINT agent_inputs_integration_origin_check CHECK (
        integration_target_binding_id IS NULL OR integration_target_id IS NOT NULL
    );

-- All writers now use explicit applications, configured routes and bindings.
-- Ownership survives in routes/workflows; the connection and target have none.
ALTER TABLE integration_installs DROP COLUMN agent_profile_id, DROP COLUMN agent_id;
ALTER TABLE integration_targets DROP COLUMN agent_id, ALTER COLUMN channel_definition_id SET NOT NULL;
CREATE UNIQUE INDEX integration_targets_target_ref_idx
    ON integration_targets(project_id, integration_install_id, target_ref);

CREATE TRIGGER integration_installs_validate_app_scope
    BEFORE INSERT OR UPDATE OF org_id, project_id, provider, integration_app_id,
        state, deleted_at, credential_secret_id
    ON integration_installs
    FOR EACH ROW
    EXECUTE FUNCTION integration_install_validate_app_scope();

CREATE TRIGGER integration_installs_identity_immutable
    BEFORE UPDATE OF id, org_id, project_id, integration_app_id, provider,
        integration_kind, provider_tenant_id, provider_account_ref,
        created_at
    ON integration_installs
    FOR EACH ROW
    EXECUTE FUNCTION reject_immutable_column_update();

CREATE TRIGGER integration_installs_advance_configuration_revision
    BEFORE UPDATE ON integration_installs
    FOR EACH ROW
    EXECUTE FUNCTION integration_install_advance_configuration_revision();

CREATE TRIGGER integration_targets_identity_immutable
    BEFORE UPDATE OF id, project_id, integration_install_id,
        target_ref, provider_ref, provider_ref_kind, parent_channel_id, channel_definition_id, created_at
    ON integration_targets
    FOR EACH ROW
    EXECUTE FUNCTION reject_immutable_column_update();

CREATE TRIGGER agent_inputs_require_integration_binding
    BEFORE INSERT ON agent_inputs
    FOR EACH ROW
    EXECUTE FUNCTION agent_input_require_integration_binding();

CREATE TRIGGER secrets_touch_integration_configuration_revisions
    AFTER UPDATE OF current_version_id ON secrets
    FOR EACH ROW
    EXECUTE FUNCTION integration_secret_touch_configuration_revisions();

-- Accepted execution facts owned by an existing tool, interaction, or turn notice.
-- Polling only projects pending obligations; this is not an outgoing work queue.
CREATE TABLE external_channel_requests (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    project_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    turn_id uuid NOT NULL,
    integration_install_id uuid NOT NULL,
    integration_target_id uuid NOT NULL,
    integration_target_binding_id uuid NOT NULL,
    tool_call_id uuid,
    interaction_id uuid,
    notice_key text,
    operation text NOT NULL,
    payload jsonb NOT NULL,
    deadline_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    state text NOT NULL DEFAULT 'pending',
    result jsonb,
    state_reason_code text,
    terminal_at timestamptz,
    CHECK (num_nonnulls(tool_call_id, interaction_id, notice_key) = 1),
    CHECK ((tool_call_id IS NOT NULL AND operation IN ('send', 'read'))
        OR (interaction_id IS NOT NULL AND operation = 'interaction')
        OR (notice_key IS NOT NULL AND operation = 'send')),
    CHECK (notice_key IS NULL OR (notice_key <> '' AND octet_length(notice_key) <= 128)),
    CHECK (payload->>'reply_channel_grants' IS NULL OR operation = 'send'),
    CHECK (jsonb_typeof(payload) = 'object' AND octet_length(payload::text) <= 524288),
    CHECK (deadline_at > created_at AND deadline_at <= created_at + interval '5 minutes'),
    CHECK (state IN ('pending', 'completed', 'canceled', 'expired')),
    CHECK ((result IS NOT NULL) = (state = 'completed')),
    CHECK (result IS NULL OR (jsonb_typeof(result) = 'object' AND octet_length(result::text) <= 1048576)),
    CHECK ((terminal_at IS NOT NULL) = (state <> 'pending')),
    CHECK (terminal_at IS NULL OR terminal_at >= created_at),
    CHECK ((state_reason_code IS NOT NULL) = (state IN ('canceled', 'expired'))),
    CHECK (state_reason_code IS NULL OR (state_reason_code <> '' AND octet_length(state_reason_code) <= 128)),
    FOREIGN KEY (project_id, agent_id) REFERENCES agents(project_id, id),
    FOREIGN KEY (agent_id, turn_id) REFERENCES agent_turns(agent_id, id),
    FOREIGN KEY (agent_id, tool_call_id) REFERENCES tool_calls(agent_id, id),
    FOREIGN KEY (agent_id, interaction_id) REFERENCES agent_interactions(agent_id, id),
    FOREIGN KEY (project_id, integration_install_id, integration_target_id)
        REFERENCES integration_targets(project_id, integration_install_id, id),
    FOREIGN KEY (project_id, agent_id, integration_target_id, integration_target_binding_id)
        REFERENCES integration_target_bindings(project_id, agent_id, integration_target_id, id)
);

CREATE UNIQUE INDEX external_channel_requests_tool_owner_idx
    ON external_channel_requests(tool_call_id) WHERE tool_call_id IS NOT NULL;
CREATE UNIQUE INDEX external_channel_requests_interaction_owner_idx
    ON external_channel_requests(interaction_id) WHERE interaction_id IS NOT NULL;
CREATE UNIQUE INDEX external_channel_requests_notice_owner_idx
    ON external_channel_requests(agent_id, turn_id, notice_key) WHERE notice_key IS NOT NULL;
CREATE INDEX external_channel_requests_pending_poll_idx
    ON external_channel_requests(project_id, integration_install_id, created_at, id) WHERE state = 'pending';
CREATE INDEX external_channel_requests_pending_expiry_idx
    ON external_channel_requests(deadline_at, id) WHERE state = 'pending';
CREATE INDEX external_channel_requests_pending_turn_idx
    ON external_channel_requests(agent_id, turn_id, id) WHERE state = 'pending';

CREATE TRIGGER external_channel_requests_facts_immutable
    BEFORE UPDATE OF id, project_id, agent_id, turn_id, integration_install_id,
        integration_target_id, integration_target_binding_id, tool_call_id,
        interaction_id, notice_key, operation, payload, deadline_at, created_at
    ON external_channel_requests
    FOR EACH ROW EXECUTE FUNCTION reject_immutable_column_update();

-- +goose StatementBegin
CREATE FUNCTION external_channel_request_enforce_transition()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'external channel request facts are immutable' USING ERRCODE = '25006';
    ELSIF TG_OP = 'INSERT' THEN
        IF NEW.state <> 'pending' THEN
            RAISE EXCEPTION 'external channel requests start pending' USING ERRCODE = '23514';
        END IF;
    ELSIF OLD.state <> 'pending' AND NEW IS DISTINCT FROM OLD THEN
        RAISE EXCEPTION 'terminal external channel requests are immutable' USING ERRCODE = '25006';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER external_channel_requests_transition
    BEFORE INSERT OR UPDATE OR DELETE ON external_channel_requests
    FOR EACH ROW EXECUTE FUNCTION external_channel_request_enforce_transition();

-- Receipt replay remembers the canonical input, whose immutable origin contains
-- the destination/binding. Outcome lifetime is exactly the receipt retention.
ALTER TABLE integration_event_receipts ADD UNIQUE (project_id, id);
CREATE TABLE integration_event_outcomes (
    project_id uuid NOT NULL,
    receipt_id uuid NOT NULL,
    delivery_key text NOT NULL CHECK (delivery_key <> '' AND octet_length(delivery_key) <= 512),
    agent_id uuid NOT NULL,
    agent_input_id uuid NOT NULL,
    PRIMARY KEY (project_id, receipt_id, delivery_key),
    FOREIGN KEY (project_id, receipt_id) REFERENCES integration_event_receipts(project_id, id) ON DELETE CASCADE,
    FOREIGN KEY (project_id, agent_id) REFERENCES agents(project_id, id),
    FOREIGN KEY (agent_id, agent_input_id) REFERENCES agent_inputs(agent_id, id)
);
CREATE TRIGGER integration_event_outcomes_immutable
    BEFORE UPDATE ON integration_event_outcomes
    FOR EACH ROW EXECUTE FUNCTION reject_immutable_column_update();
