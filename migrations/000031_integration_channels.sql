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

ALTER TABLE integration_installs
    DROP CONSTRAINT integration_installs_provider_check,
    DROP CONSTRAINT integration_installs_provider_tenant_id_check,
    DROP CONSTRAINT integration_installs_check,
    ADD CONSTRAINT integration_installs_provider_check
        CHECK (provider ~ '^[a-z0-9][a-z0-9_.-]{0,127}$'),
    ADD CONSTRAINT integration_installs_destination_check
        CHECK (agent_profile_id IS NULL OR agent_id IS NULL),
    ADD CONSTRAINT integration_installs_channel_payload_bounds_check CHECK (
        octet_length(integration_kind) <= 128
        AND octet_length(connection_mode) <= 128
        AND octet_length(provider_tenant_id) <= 512
        AND octet_length(provider_account_ref) <= 512
        AND octet_length(provider_agent_display_name) <= 512
        AND octet_length(provider_config::text) <= 262144
        AND octet_length(provider_identity::text) <= 262144
        AND octet_length(provider_metadata::text) <= 262144
    ),
    ADD COLUMN integration_app_id uuid,
    ADD COLUMN configuration_revision bigint NOT NULL DEFAULT 1,
    ADD CONSTRAINT integration_installs_configuration_revision_check
        CHECK (configuration_revision > 0),
    ADD CONSTRAINT integration_installs_project_id_id_app_key
        UNIQUE (project_id, id, integration_app_id);

ALTER TABLE integration_targets
    ALTER COLUMN agent_id DROP NOT NULL,
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

-- Connector targets have no agent projection, so NULL must participate in the
-- target-ref identity that the target creator retries on collision.
DROP INDEX integration_targets_agent_target_ref_idx;
CREATE UNIQUE INDEX integration_targets_agent_target_ref_idx
    ON integration_targets(project_id, agent_id, target_ref) NULLS NOT DISTINCT;

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
CREATE FUNCTION reject_immutable_integration_column_update()
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
    EXECUTE FUNCTION reject_immutable_integration_column_update();

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

-- Old API instances and the native Slack path omit integration_app_id while
-- completing OAuth. Keep this compatibility trigger until native Slack moves
-- to explicit app registration.
-- +goose StatementBegin
CREATE FUNCTION integration_install_fill_compatibility_app()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    app_id uuid;
BEGIN
    IF NEW.integration_app_id IS NOT NULL THEN
        RETURN NEW;
    END IF;
    IF NEW.provider <> 'slack' THEN
        RAISE EXCEPTION 'integration app is required for connector installations'
            USING ERRCODE = '23514';
    END IF;

    -- Old API binaries do not know about integration_apps or the shared side
    -- of the project lifecycle protocol. Acquire it here before creating the
    -- compatibility app. The project row lock also serializes with deletion by
    -- binaries old enough not to use the advisory lock at all.
    PERFORM pg_advisory_xact_lock_shared(hashtextextended(NEW.project_id::text, 0));
    PERFORM 1
    FROM projects project
    JOIN orgs organization ON organization.id = project.org_id
    WHERE project.org_id = NEW.org_id
      AND project.id = NEW.project_id
      AND project.deleted_at IS NULL
      AND organization.deleted_at IS NULL
    FOR SHARE OF project, organization;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'integration installation requires an active project'
            USING ERRCODE = '23503';
    END IF;

    SELECT app.id INTO app_id
    FROM integration_apps app
    WHERE app.org_id = NEW.org_id
      AND app.owner_project_id = NEW.project_id
      AND app.provider = NEW.provider
      AND app.provider_app_ref = NEW.provider_account_ref
      AND app.deleted_at IS NULL;

    IF app_id IS NULL THEN
        INSERT INTO integration_apps(
            org_id, owner_project_id, provider, provider_app_ref, display_name,
            connector_key, installation_credential_kind, state, created_at, updated_at
        ) VALUES (
            NEW.org_id, NEW.project_id, NEW.provider, NEW.provider_account_ref,
            NEW.provider_agent_display_name,
            'native_slack_v1',
            'slack_app_credentials',
            'active',
            transaction_timestamp(), transaction_timestamp()
        )
        ON CONFLICT (owner_project_id, provider, provider_app_ref)
            WHERE owner_project_id IS NOT NULL AND deleted_at IS NULL
        DO UPDATE SET updated_at = statement_timestamp()
        RETURNING id INTO app_id;
    END IF;

    NEW.integration_app_id := app_id;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

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
    IF app.connector_key LIKE 'native\_%' ESCAPE '\' THEN
        IF (NEW.agent_profile_id IS NULL) = (NEW.agent_id IS NULL) THEN
            RAISE EXCEPTION 'native integration installation requires exactly one destination'
                USING ERRCODE = '23514';
        END IF;
    ELSIF NEW.agent_profile_id IS NOT NULL OR NEW.agent_id IS NOT NULL THEN
        RAISE EXCEPTION 'connector integration installation cannot own an agent destination'
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
-- agent or profile; matching, attachment, launching, and intentional fanout
-- belong to the registered, versioned handler.
CREATE TABLE integration_routes (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    project_id uuid NOT NULL,
    integration_install_id uuid NOT NULL,
    deployment_key text NOT NULL,
    handler_key text NOT NULL,
    handler_version integer NOT NULL,
    configuration jsonb NOT NULL DEFAULT '{}'::jsonb,
    state text NOT NULL,
    deleted_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK (deployment_key <> '' AND octet_length(deployment_key) <= 512),
    CHECK (handler_key ~ '^[a-z0-9][a-z0-9_.-]{0,127}$'),
    CHECK (handler_version > 0),
    CHECK (jsonb_typeof(configuration) = 'object'),
    CONSTRAINT integration_routes_configuration_bytes_check
        CHECK (octet_length(configuration::text) <= 262144),
    CHECK (state IN ('active', 'disabled')),
    FOREIGN KEY (project_id, integration_install_id) REFERENCES integration_installs(project_id, id),
    UNIQUE (project_id, integration_install_id, id),
    UNIQUE (project_id, integration_install_id, deployment_key)
);

CREATE INDEX integration_routes_active_install_idx
    ON integration_routes(project_id, integration_install_id, created_at, id)
    WHERE state = 'active' AND deleted_at IS NULL;

CREATE TRIGGER integration_routes_definition_immutable
    BEFORE UPDATE OF id, project_id, integration_install_id, deployment_key,
        handler_key, handler_version, configuration, created_at
    ON integration_routes
    FOR EACH ROW
    EXECUTE FUNCTION reject_immutable_integration_column_update();

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
       OR OLD.provider_agent_display_name IS DISTINCT FROM NEW.provider_agent_display_name
       OR OLD.credential_secret_id IS DISTINCT FROM NEW.credential_secret_id
       OR OLD.provider_config IS DISTINCT FROM NEW.provider_config
       OR OLD.provider_identity IS DISTINCT FROM NEW.provider_identity
       OR OLD.provider_metadata IS DISTINCT FROM NEW.provider_metadata
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
    send_allowed boolean NOT NULL,
    source text NOT NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK (receive_allowed OR send_allowed),
    CHECK (
        NOT receive_allowed
        OR integration_route_id IS NOT NULL
        OR source = 'legacy_target'
    ),
    CHECK (source <> 'legacy_target' OR integration_route_id IS NULL),
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

-- Route-less receive authority is reserved for targets created by the native
-- compatibility path. Connector-managed targets must obtain receive authority
-- from a real route even when a caller writes the table directly.
-- +goose StatementBegin
CREATE FUNCTION integration_target_binding_validate_legacy_shape()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    valid_legacy_shape boolean;
BEGIN
    IF NEW.source <> 'legacy_target' THEN
        RETURN NEW;
    END IF;

    SELECT EXISTS (
        SELECT 1
        FROM integration_targets target
        JOIN integration_installs install
          ON install.project_id = target.project_id
         AND install.id = target.integration_install_id
        JOIN integration_apps app
          ON app.org_id = install.org_id
         AND app.id = install.integration_app_id
        WHERE target.project_id = NEW.project_id
          AND target.integration_install_id = NEW.integration_install_id
          AND target.id = NEW.integration_target_id
          AND target.agent_id = NEW.agent_id
          AND app.connector_key LIKE 'native\_%' ESCAPE '\'
    ) INTO valid_legacy_shape;

    IF NOT valid_legacy_shape THEN
        RAISE EXCEPTION 'legacy integration binding requires its native target owner'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER integration_target_bindings_validate_legacy_shape
    BEFORE INSERT ON integration_target_bindings
    FOR EACH ROW
    EXECUTE FUNCTION integration_target_binding_validate_legacy_shape();

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
        receive_allowed, send_allowed, source, metadata, created_at
    ON integration_target_bindings
    FOR EACH ROW
    EXECUTE FUNCTION reject_immutable_integration_column_update();

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

CREATE INDEX integration_target_bindings_agent_target_order_idx
    ON integration_target_bindings(
        project_id, agent_id, target_created_at DESC, integration_target_id DESC
    )
    WHERE revoked_at IS NULL;

CREATE INDEX integration_target_bindings_target_receive_idx
    ON integration_target_bindings(project_id, integration_target_id, integration_route_id, id)
    WHERE receive_allowed AND revoked_at IS NULL;

CREATE INDEX integration_target_bindings_install_idx
    ON integration_target_bindings(project_id, integration_install_id, id)
    WHERE revoked_at IS NULL;

-- Native compatibility targets retain the legacy creator projection. Modern
-- connector targets are project-owned, so bindings are their only agent link.
-- +goose StatementBegin
CREATE FUNCTION integration_target_validate_install_shape()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    native_path boolean;
BEGIN
    SELECT app.connector_key LIKE 'native\_%' ESCAPE '\' INTO native_path
    FROM integration_installs install
    JOIN integration_apps app
      ON app.org_id = install.org_id
     AND app.id = install.integration_app_id
    WHERE install.project_id = NEW.project_id
      AND install.id = NEW.integration_install_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'integration target installation does not exist'
            USING ERRCODE = '23503';
    END IF;
    IF (native_path AND NEW.agent_id IS NULL)
       OR (NOT native_path AND NEW.agent_id IS NOT NULL) THEN
        RAISE EXCEPTION 'integration target ownership does not match its connector path'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- Old workers create targets without a binding. Only the native compatibility
-- path receives this implicit creator binding; connector routes write theirs explicitly.
-- +goose StatementBegin
CREATE FUNCTION integration_target_create_legacy_binding()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    INSERT INTO integration_target_bindings(
        project_id, agent_id, integration_install_id, integration_target_id,
        target_created_at, integration_route_id,
        receive_allowed, send_allowed, source,
        created_at, updated_at
    )
    SELECT NEW.project_id, NEW.agent_id, NEW.integration_install_id, NEW.id,
           NEW.created_at, NULL, true, true, 'legacy_target',
           NEW.created_at, NEW.updated_at
    FROM integration_installs install
    JOIN integration_apps app
      ON app.org_id = install.org_id
     AND app.id = install.integration_app_id
    WHERE install.project_id = NEW.project_id
      AND install.id = NEW.integration_install_id
      AND NEW.agent_id IS NOT NULL
      AND app.connector_key LIKE 'native\_%' ESCAPE '\'
    ON CONFLICT DO NOTHING;

    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- Historical inputs deliberately retain NULL binding provenance. Rewriting the
-- hot immutable input ledger would create an avoidable deployment hazard, while
-- PostgreSQL still enforces the new binding foreign key for every new row.

-- Old workers omit binding provenance. Resolve the creator binding before the
-- immutable input row is inserted; new connector code always supplies it explicitly.
-- +goose StatementBegin
CREATE FUNCTION agent_input_fill_integration_binding()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.integration_target_id IS NOT NULL AND NEW.integration_target_binding_id IS NULL THEN
        SELECT binding.id INTO NEW.integration_target_binding_id
        FROM integration_target_bindings binding
        WHERE binding.project_id = NEW.project_id
          AND binding.agent_id = NEW.agent_id
          AND binding.integration_target_id = NEW.integration_target_id
          AND binding.receive_allowed
          AND binding.source = 'legacy_target'
          AND binding.integration_route_id IS NULL
          AND binding.revoked_at IS NULL
        ORDER BY binding.created_at, binding.id
        LIMIT 1;
        IF NEW.integration_target_binding_id IS NULL THEN
            RAISE EXCEPTION 'target-backed agent input requires an active binding'
                USING ERRCODE = '23514';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER agent_inputs_integration_binding_immutable
    BEFORE UPDATE OF integration_target_binding_id ON agent_inputs
    FOR EACH ROW
    WHEN (OLD.integration_target_binding_id IS DISTINCT FROM NEW.integration_target_binding_id)
    EXECUTE FUNCTION reject_immutable_integration_column_update();

-- Connector-backed sends use a project-shard-local outbox. Native Slack remains
-- inline in this release and can opt into the same table later without a migration.
CREATE TABLE integration_deliveries (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    project_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    integration_app_id uuid NOT NULL,
    integration_install_id uuid NOT NULL,
    integration_target_id uuid NOT NULL,
    integration_target_binding_id uuid NOT NULL,
    provider text NOT NULL,
    connector_key text NOT NULL,
    transport text NOT NULL,
    delivery_kind text NOT NULL,
    payload_version text NOT NULL,
    payload jsonb NOT NULL,
    idempotency_scope text NOT NULL,
    idempotency_key text NOT NULL,
    state text NOT NULL,
    attempt_count integer NOT NULL DEFAULT 0,
    available_at timestamptz NOT NULL,
    claim_token uuid,
    claim_generation bigint NOT NULL DEFAULT 0,
    claimed_by text,
    claimed_at timestamptz,
    claim_expires_at timestamptz,
    notify_ref uuid,
    provider_message_ref text,
    last_error jsonb NOT NULL DEFAULT '{}'::jsonb,
    completed_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK (provider ~ '^[a-z0-9][a-z0-9_.-]{0,127}$'),
    CHECK (connector_key ~ '^[a-z0-9][a-z0-9_.-]{0,127}$'),
    CHECK (transport IN ('connector', 'native')),
    CHECK (delivery_kind <> '' AND octet_length(delivery_kind) <= 128),
    CHECK (payload_version <> '' AND octet_length(payload_version) <= 128),
    CHECK (jsonb_typeof(payload) = 'object'),
    CONSTRAINT integration_deliveries_payload_bytes_check
        CHECK (octet_length(payload::text) <= 262144),
    CHECK (idempotency_scope <> '' AND octet_length(idempotency_scope) <= 512),
    CHECK (idempotency_key <> '' AND octet_length(idempotency_key) <= 512),
    CHECK (claimed_by IS NULL OR octet_length(claimed_by) <= 256),
    CHECK (provider_message_ref IS NULL OR octet_length(provider_message_ref) <= 2048),
    CHECK (state IN ('pending', 'claimed', 'retry_wait', 'delivered', 'failed', 'unknown', 'canceled')),
    CHECK (attempt_count >= 0),
    CHECK (claim_generation >= 0),
    CHECK (jsonb_typeof(last_error) = 'object'),
    CONSTRAINT integration_deliveries_last_error_bytes_check
        CHECK (octet_length(last_error::text) <= 262144),
    CHECK (
        (state = 'claimed') =
        (claim_token IS NOT NULL AND claimed_by IS NOT NULL AND claimed_at IS NOT NULL AND claim_expires_at IS NOT NULL)
    ),
    CHECK ((state IN ('delivered', 'failed', 'unknown', 'canceled')) = (completed_at IS NOT NULL)),
    FOREIGN KEY (project_id, agent_id) REFERENCES agents(project_id, id),
    FOREIGN KEY (project_id, integration_install_id, integration_app_id)
        REFERENCES integration_installs(project_id, id, integration_app_id),
    FOREIGN KEY (project_id, integration_install_id, integration_target_id)
        REFERENCES integration_targets(project_id, integration_install_id, id),
    FOREIGN KEY (project_id, agent_id, integration_target_id, integration_target_binding_id)
        REFERENCES integration_target_bindings(project_id, agent_id, integration_target_id, id),
    UNIQUE (project_id, agent_id, idempotency_scope, idempotency_key)
);

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

INSERT INTO integration_sweep_cursors(
    sweep_kind, last_item_id, cycle_end_id, updated_at
)
VALUES
    ('delivery_unavailable', '00000000-0000-0000-0000-000000000000', NULL, transaction_timestamp());

CREATE INDEX integration_deliveries_due_connector_idx
    ON integration_deliveries(connector_key, provider, available_at, id)
    WHERE transport = 'connector' AND state IN ('pending', 'retry_wait');

CREATE INDEX integration_deliveries_unavailable_sweep_idx
    ON integration_deliveries(id)
    WHERE transport = 'connector' AND state IN ('pending', 'retry_wait');

CREATE INDEX integration_deliveries_expired_claim_idx
    ON integration_deliveries(claim_expires_at, id)
    WHERE state = 'claimed';

CREATE INDEX integration_deliveries_terminal_retention_idx
    ON integration_deliveries(completed_at, id)
    WHERE state IN ('delivered', 'failed', 'unknown', 'canceled');

-- +goose StatementBegin
CREATE FUNCTION integration_deliveries_reject_terminal_change()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.state IN ('delivered', 'failed', 'unknown', 'canceled') AND NEW IS DISTINCT FROM OLD THEN
        RAISE EXCEPTION 'terminal integration deliveries are immutable'
            USING ERRCODE = '25006';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER integration_deliveries_intent_immutable
    BEFORE UPDATE OF id, project_id, agent_id, integration_app_id,
        integration_install_id, integration_target_id,
        integration_target_binding_id, provider, connector_key, transport,
        delivery_kind, payload_version, payload, idempotency_scope,
        idempotency_key, notify_ref, created_at
    ON integration_deliveries
    FOR EACH ROW
    EXECUTE FUNCTION reject_immutable_integration_column_update();

CREATE TRIGGER integration_deliveries_terminal_immutable
    BEFORE UPDATE ON integration_deliveries
    FOR EACH ROW
    EXECUTE FUNCTION integration_deliveries_reject_terminal_change();

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
    EXECUTE FUNCTION reject_immutable_integration_column_update();

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

-- Convert the supported legacy installation shape in three set-based steps.
-- Existing apps are project-restricted because native credentials belong to
-- the project that completed OAuth.
INSERT INTO integration_apps(
    org_id, owner_project_id, provider, provider_app_ref, display_name,
    connector_key, installation_credential_kind, state, deleted_at,
    created_at, updated_at
)
SELECT install.org_id,
       install.project_id,
       install.provider,
       install.provider_account_ref,
       max(install.provider_agent_display_name),
       left('native_' || install.provider || '_v1', 128),
       CASE install.provider
         WHEN 'slack' THEN 'slack_app_credentials'
         ELSE 'integration_credentials'
       END,
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
    ALTER COLUMN integration_app_id SET NOT NULL,
    ADD CONSTRAINT integration_installs_app_fkey
        FOREIGN KEY (org_id, integration_app_id)
        REFERENCES integration_apps(org_id, id);

CREATE UNIQUE INDEX integration_installs_app_tenant_account_idx
    ON integration_installs(
        integration_app_id, provider_tenant_id, provider_account_ref
    )
    WHERE deleted_at IS NULL;

INSERT INTO integration_target_bindings(
    project_id, agent_id, integration_install_id, integration_target_id,
    target_created_at, integration_route_id,
    receive_allowed, send_allowed, source, revoked_at,
    created_at, updated_at
)
SELECT target.project_id,
       target.agent_id,
       target.integration_install_id,
       target.id,
       target.created_at,
       NULL,
       true,
       true,
       'legacy_target',
       coalesce(target.deleted_at, install.deleted_at, app.deleted_at),
       target.created_at,
       greatest(target.updated_at, install.updated_at, app.updated_at)
FROM integration_targets target
JOIN integration_installs install
  ON install.project_id = target.project_id
 AND install.id = target.integration_install_id
JOIN integration_apps app
  ON app.org_id = install.org_id
 AND app.id = install.integration_app_id;

-- Historical inputs intentionally retain NULL binding provenance. New native
-- writes fill it before insert; connector writes always provide it explicitly.
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

-- Compatibility triggers remain while the native Slack path omits explicit
-- app, route, and binding provenance. They synthesize the compatibility app
-- and route-less legacy binding; new target-backed inputs inherit that binding.
CREATE TRIGGER integration_installs_00_fill_compatibility_app
    BEFORE INSERT ON integration_installs
    FOR EACH ROW
    EXECUTE FUNCTION integration_install_fill_compatibility_app();

CREATE TRIGGER integration_installs_validate_app_scope
    BEFORE INSERT OR UPDATE OF org_id, project_id, provider, integration_app_id,
        agent_profile_id, agent_id, state, deleted_at, credential_secret_id
    ON integration_installs
    FOR EACH ROW
    EXECUTE FUNCTION integration_install_validate_app_scope();

CREATE TRIGGER integration_installs_identity_immutable
    BEFORE UPDATE OF id, org_id, project_id, integration_app_id, provider,
        integration_kind, provider_tenant_id, provider_account_ref,
        agent_profile_id, agent_id, created_at
    ON integration_installs
    FOR EACH ROW
    EXECUTE FUNCTION reject_immutable_integration_column_update();

CREATE TRIGGER integration_installs_advance_configuration_revision
    BEFORE UPDATE ON integration_installs
    FOR EACH ROW
    EXECUTE FUNCTION integration_install_advance_configuration_revision();

CREATE TRIGGER integration_targets_identity_immutable
    BEFORE UPDATE OF id, project_id, agent_id, integration_install_id,
        target_ref, provider_ref, provider_ref_kind, created_at
    ON integration_targets
    FOR EACH ROW
    EXECUTE FUNCTION reject_immutable_integration_column_update();

CREATE TRIGGER integration_targets_validate_install_shape
    BEFORE INSERT ON integration_targets
    FOR EACH ROW
    EXECUTE FUNCTION integration_target_validate_install_shape();

CREATE TRIGGER integration_targets_create_legacy_binding
    AFTER INSERT ON integration_targets
    FOR EACH ROW
    EXECUTE FUNCTION integration_target_create_legacy_binding();

CREATE TRIGGER agent_inputs_fill_integration_binding
    BEFORE INSERT ON agent_inputs
    FOR EACH ROW
    EXECUTE FUNCTION agent_input_fill_integration_binding();

CREATE TRIGGER secrets_touch_integration_configuration_revisions
    AFTER UPDATE OF current_version_id ON secrets
    FOR EACH ROW
    EXECUTE FUNCTION integration_secret_touch_configuration_revisions();
