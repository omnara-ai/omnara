-- +goose Up

-- Identity, organization, project, membership, and token authority.
-- Omnara initial canonical state.
--
-- Postgres is the product source of truth. Provider-native observations can be stored
-- as artifact/raw payload data, but they must not replace these product tables.

CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE users (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    display_name text NOT NULL DEFAULT '',
    deleted_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);

CREATE INDEX users_created_idx
    ON users(created_at, id);

CREATE TABLE orgs (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    name text NOT NULL,
    idempotency_key text,
    deleted_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (idempotency_key)
);

CREATE TABLE installation (
    singleton_key smallint PRIMARY KEY DEFAULT 1,
    id uuid NOT NULL UNIQUE DEFAULT uuidv7(),
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    CHECK (singleton_key = 1)
);

INSERT INTO installation DEFAULT VALUES;

CREATE TABLE user_emails (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    user_id uuid NOT NULL REFERENCES users(id),
    email text NOT NULL,
    normalized_email text NOT NULL,
    verified_at timestamptz,
    is_primary boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (user_id, id),
    UNIQUE (user_id, normalized_email),
    CHECK (email <> ''),
    CHECK (normalized_email <> '')
);

CREATE UNIQUE INDEX user_emails_one_primary_per_user
    ON user_emails(user_id)
    WHERE is_primary;

CREATE UNIQUE INDEX user_emails_one_verified_user_per_email
    ON user_emails(normalized_email)
    WHERE verified_at IS NOT NULL;

CREATE INDEX user_emails_normalized_email_id_idx
    ON user_emails(normalized_email, id);

CREATE TABLE user_credentials (
    user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    password_hash text NOT NULL,
    password_changed_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK (password_hash <> '')
);

CREATE TABLE user_auth_tokens (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    user_email_id uuid,
    purpose text NOT NULL,
    token_hash text NOT NULL,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    UNIQUE (token_hash),
    CHECK (purpose IN ('email_verification', 'password_reset')),
    CHECK (token_hash <> ''),
    CHECK (expires_at > created_at),
    CHECK ((purpose = 'email_verification' AND user_email_id IS NOT NULL) OR (purpose = 'password_reset' AND user_email_id IS NULL)),
    FOREIGN KEY (user_id, user_email_id) REFERENCES user_emails(user_id, id) ON DELETE CASCADE
);

CREATE INDEX user_auth_tokens_cleanup_idx
    ON user_auth_tokens(expires_at, id);

CREATE INDEX user_auth_tokens_user_purpose_idx
    ON user_auth_tokens(user_id, purpose, created_at DESC);

CREATE INDEX user_auth_tokens_email_idx
    ON user_auth_tokens(user_id, user_email_id)
    WHERE user_email_id IS NOT NULL;

CREATE UNIQUE INDEX user_auth_tokens_one_active_password_reset_per_user
    ON user_auth_tokens(user_id)
    WHERE purpose = 'password_reset' AND consumed_at IS NULL;

CREATE TABLE auth_connectors (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    slug text NOT NULL,
    kind text NOT NULL,
    display_name text NOT NULL,
    issuer text NOT NULL,
    authorization_url text NOT NULL DEFAULT '',
    token_url text NOT NULL DEFAULT '',
    userinfo_url text NOT NULL DEFAULT '',
    client_id text NOT NULL,
    encrypted_client_secret jsonb NOT NULL,
    scopes text[] NOT NULL DEFAULT '{}'::text[],
    email_trust_policy text NOT NULL DEFAULT 'none',
    enabled boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (slug),
    UNIQUE (issuer),
    UNIQUE (id, issuer),
    CHECK (slug <> ''),
    CHECK (kind IN ('oidc', 'github')),
    CHECK (email_trust_policy IN ('none', 'verified_email')),
    CHECK (display_name <> ''),
    CHECK (issuer <> ''),
    CHECK (client_id <> ''),
    CHECK (jsonb_typeof(encrypted_client_secret) = 'object'),
    CHECK (
        (kind = 'oidc' AND authorization_url = '' AND token_url = '' AND userinfo_url = '')
        OR
        (kind = 'github' AND authorization_url <> '' AND token_url <> '' AND userinfo_url <> '')
    )
);

-- +goose StatementBegin
CREATE FUNCTION auth_connectors_reject_identity_change() RETURNS trigger AS $$
BEGIN
    IF NEW.id IS DISTINCT FROM OLD.id
       OR NEW.slug IS DISTINCT FROM OLD.slug
       OR NEW.kind IS DISTINCT FROM OLD.kind
       OR NEW.issuer IS DISTINCT FROM OLD.issuer THEN
        RAISE EXCEPTION 'auth connector identity is immutable'
            USING ERRCODE = '25006';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER auth_connectors_identity_immutable
    BEFORE UPDATE OF id, slug, kind, issuer ON auth_connectors
    FOR EACH ROW
    EXECUTE FUNCTION auth_connectors_reject_identity_change();

CREATE INDEX auth_connectors_enabled_idx
    ON auth_connectors(enabled, slug);

CREATE TABLE user_auth_identities (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    user_id uuid NOT NULL REFERENCES users(id),
    auth_connector_id uuid NOT NULL,
    issuer text NOT NULL,
    subject text NOT NULL,
    email_at_link text NOT NULL DEFAULT '',
    email_verified boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL,
    UNIQUE (auth_connector_id, subject),
    UNIQUE (user_id, auth_connector_id),
    CHECK (issuer <> ''),
    CHECK (subject <> ''),
    FOREIGN KEY (auth_connector_id, issuer) REFERENCES auth_connectors(id, issuer)
);

CREATE TABLE personal_access_tokens (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    user_id uuid NOT NULL REFERENCES users(id),
    name text NOT NULL,
    token_id text NOT NULL,
    token_hash text NOT NULL,
    created_at timestamptz NOT NULL,
    last_used_at timestamptz,
    revoked_at timestamptz,
    UNIQUE (token_id),
    UNIQUE (token_hash),
    CHECK (name <> ''),
    CHECK (token_id <> ''),
    CHECK (token_hash <> '')
);

CREATE INDEX personal_access_tokens_active_user_idx
    ON personal_access_tokens(user_id)
    WHERE revoked_at IS NULL;

CREATE INDEX personal_access_tokens_user_created_idx
    ON personal_access_tokens(user_id, created_at DESC, id DESC);

-- Org API keys are org-owned account principals. created_by_user_id records
-- provenance only; the key's authority and lifetime are independent of the
-- creating user.
CREATE TABLE org_api_keys (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id uuid NOT NULL REFERENCES orgs(id),
    name text NOT NULL,
    token_id text NOT NULL,
    token_hash text NOT NULL,
    created_by_user_id uuid NOT NULL REFERENCES users(id),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    last_used_at timestamptz,
    revoked_at timestamptz,
    UNIQUE (token_id),
    UNIQUE (token_hash),
    UNIQUE (org_id, id),
    CHECK (name <> ''),
    CHECK (token_id <> ''),
    CHECK (token_hash <> '')
);

CREATE UNIQUE INDEX org_api_keys_active_org_name_idx
    ON org_api_keys(org_id, name)
    WHERE revoked_at IS NULL;

CREATE INDEX org_api_keys_org_created_idx
    ON org_api_keys(org_id, created_at DESC, id DESC);

CREATE TABLE projects (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id uuid NOT NULL REFERENCES orgs(id),
    name text NOT NULL,
    idempotency_key text,
    deleted_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (org_id, id),
    UNIQUE (org_id, idempotency_key)
);

CREATE UNIQUE INDEX projects_active_name_idx ON projects(org_id, name)
    WHERE deleted_at IS NULL;

CREATE INDEX projects_org_created_idx
    ON projects(org_id, created_at DESC, id DESC);
CREATE INDEX projects_name_trgm_idx ON projects USING gin (name gin_trgm_ops)
    WHERE deleted_at IS NULL;

-- +goose StatementBegin
CREATE FUNCTION projects_reject_identity_change() RETURNS trigger AS $$
BEGIN
    IF NEW.id IS DISTINCT FROM OLD.id
       OR NEW.org_id IS DISTINCT FROM OLD.org_id THEN
        RAISE EXCEPTION 'project identity is immutable'
            USING ERRCODE = '25006';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER projects_identity_immutable
    BEFORE UPDATE OF id, org_id ON projects
    FOR EACH ROW
    EXECUTE FUNCTION projects_reject_identity_change();

CREATE TABLE actors (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    project_id uuid NOT NULL,
    provider text NOT NULL,
    provider_tenant_id text,
    provider_user_id text NOT NULL,
    display_name text,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK (provider IN ('omnara', 'slack', 'external')),
    CHECK (provider = 'external' OR provider_tenant_id IS NOT NULL),
    CHECK (provider_tenant_id IS NULL OR provider_tenant_id <> ''),
    CHECK (provider_user_id <> ''),
    CHECK (display_name IS NULL OR display_name <> ''),
    CHECK (jsonb_typeof(metadata) = 'object'),
    FOREIGN KEY (project_id) REFERENCES projects(id),
    UNIQUE (project_id, id)
);

CREATE UNIQUE INDEX actors_provider_identity_idx
    ON actors(project_id, provider, provider_tenant_id, provider_user_id)
    NULLS NOT DISTINCT;

CREATE INDEX actors_project_created_idx
    ON actors(project_id, created_at DESC, id DESC);

-- A membership row belongs to exactly one principal: a user or an org API
-- key. The composite key FK pins key memberships to the key's own org, and
-- keys can never hold the owner role.
CREATE TABLE org_memberships (
    id uuid NOT NULL DEFAULT uuidv7(),
    org_id uuid NOT NULL REFERENCES orgs(id),
    user_id uuid REFERENCES users(id),
    org_api_key_id uuid,
    role text NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (id),
    UNIQUE (org_id, id),
    CHECK (num_nonnulls(user_id, org_api_key_id) = 1),
    CHECK (role IN ('owner', 'admin', 'member')),
    CHECK (org_api_key_id IS NULL OR role IN ('admin', 'member')),
    FOREIGN KEY (org_id, org_api_key_id) REFERENCES org_api_keys(org_id, id)
);

CREATE UNIQUE INDEX org_memberships_org_user_idx
    ON org_memberships(org_id, user_id)
    WHERE user_id IS NOT NULL;

CREATE UNIQUE INDEX org_memberships_org_api_key_idx
    ON org_memberships(org_id, org_api_key_id)
    WHERE org_api_key_id IS NOT NULL;

CREATE INDEX org_memberships_user_idx
    ON org_memberships(user_id)
    WHERE user_id IS NOT NULL;

CREATE INDEX org_memberships_org_created_idx
    ON org_memberships(org_id, created_at DESC);

-- Project roles hang off the org membership row, so a project member is an
-- org member by construction and removing the org membership removes its
-- project roles.
CREATE TABLE project_memberships (
    org_id uuid NOT NULL,
    project_id uuid NOT NULL,
    org_membership_id uuid NOT NULL,
    role text NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (project_id, org_membership_id),
    CHECK (role IN ('admin', 'developer', 'operator', 'viewer')),
    FOREIGN KEY (org_id, project_id) REFERENCES projects(org_id, id),
    FOREIGN KEY (org_id, org_membership_id) REFERENCES org_memberships(org_id, id) ON DELETE CASCADE
);

CREATE INDEX project_memberships_org_membership_idx
    ON project_memberships(org_id, org_membership_id);

-- +goose StatementBegin
CREATE FUNCTION secret_metadata_is_string_map(metadata jsonb) RETURNS boolean AS $$
    SELECT jsonb_typeof(metadata) = 'object'
       AND NOT EXISTS (
           SELECT 1
           FROM jsonb_each(metadata) AS item(key, value)
           WHERE jsonb_typeof(item.value) <> 'string'
       );
$$ LANGUAGE sql IMMUTABLE;
-- +goose StatementEnd

CREATE TABLE secrets (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id uuid NOT NULL REFERENCES orgs(id),
    management_kind text NOT NULL,
    owner_kind text NOT NULL,
    owner_project_id uuid,
    owner_user_id uuid REFERENCES users(id),
    name text NOT NULL,
    kind text NOT NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    current_version_id uuid,
    deleted_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (org_id, id),
    UNIQUE (org_id, id, management_kind),
    -- Deleting a secret soft deletes the row but destroys its versions; live
    -- secrets always point at a current version.
    CHECK (deleted_at IS NOT NULL OR current_version_id IS NOT NULL),
    CHECK (management_kind IN ('tenant', 'cluster')),
    CHECK (management_kind = 'tenant' OR owner_kind = 'org'),
    CHECK (owner_kind IN ('org', 'project', 'user')),
    CHECK (kind IN ('generic', 'oauth_token_set', 'slack_app_credentials')),
    CHECK (name <> ''),
    CHECK (secret_metadata_is_string_map(metadata)),
    CHECK ((owner_kind = 'org') = (owner_project_id IS NULL AND owner_user_id IS NULL)),
    CHECK ((owner_kind = 'project') = (owner_project_id IS NOT NULL AND owner_user_id IS NULL)),
    CHECK ((owner_kind = 'user') = (owner_project_id IS NULL AND owner_user_id IS NOT NULL)),
    FOREIGN KEY (org_id, owner_project_id) REFERENCES projects(org_id, id)
);

CREATE UNIQUE INDEX secrets_org_owner_name_idx
    ON secrets(org_id, name)
    WHERE owner_kind = 'org' AND deleted_at IS NULL;

CREATE UNIQUE INDEX secrets_project_owner_name_idx
    ON secrets(org_id, owner_project_id, name)
    WHERE owner_kind = 'project' AND deleted_at IS NULL;

CREATE UNIQUE INDEX secrets_user_owner_name_idx
    ON secrets(org_id, owner_user_id, name)
    WHERE owner_kind = 'user' AND deleted_at IS NULL;

CREATE INDEX secrets_org_owner_created_idx
    ON secrets(org_id, created_at DESC, id DESC)
    WHERE owner_kind = 'org';

CREATE INDEX secrets_project_owner_created_idx
    ON secrets(org_id, owner_project_id, created_at DESC, id DESC)
    WHERE owner_kind = 'project';

CREATE INDEX secrets_user_owner_created_idx
    ON secrets(owner_user_id, org_id, created_at DESC, id DESC)
    WHERE owner_kind = 'user';

CREATE INDEX secrets_metadata_gin_idx
    ON secrets USING gin (metadata jsonb_path_ops)
    WHERE deleted_at IS NULL;
CREATE INDEX secrets_name_trgm_idx ON secrets USING gin (name gin_trgm_ops)
    WHERE deleted_at IS NULL;

-- +goose StatementBegin
CREATE FUNCTION secrets_reject_authority_change() RETURNS trigger AS $$
BEGIN
    IF NEW.id IS DISTINCT FROM OLD.id
       OR NEW.org_id IS DISTINCT FROM OLD.org_id
       OR NEW.management_kind IS DISTINCT FROM OLD.management_kind
       OR NEW.kind IS DISTINCT FROM OLD.kind
       OR NEW.owner_kind IS DISTINCT FROM OLD.owner_kind
       OR NEW.owner_project_id IS DISTINCT FROM OLD.owner_project_id
       OR NEW.owner_user_id IS DISTINCT FROM OLD.owner_user_id THEN
        RAISE EXCEPTION 'secret authority is immutable'
            USING ERRCODE = '25006';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER secrets_authority_immutable
    BEFORE UPDATE OF id, org_id, management_kind, kind, owner_kind, owner_project_id, owner_user_id ON secrets
    FOR EACH ROW
    EXECUTE FUNCTION secrets_reject_authority_change();

CREATE TABLE secret_versions (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id uuid NOT NULL,
    secret_id uuid NOT NULL,
    version_number integer NOT NULL,
    payload_keys text[] NOT NULL,
    encryption_scheme text NOT NULL,
    key_id text NOT NULL,
    dek_wrapped_by text NOT NULL,
    encrypted_dek bytea NOT NULL,
    encrypted_dek_nonce bytea NOT NULL,
    nonce bytea NOT NULL,
    ciphertext bytea NOT NULL,
    created_at timestamptz NOT NULL,
    mcp_oauth_flow_id uuid,
    oauth_access_token_expires_at timestamptz,
    UNIQUE (secret_id, id),
    UNIQUE (secret_id, version_number),
    CHECK (version_number > 0),
    CHECK (cardinality(payload_keys) > 0),
    CHECK (array_position(payload_keys, NULL) IS NULL),
    CHECK (array_position(payload_keys, '') IS NULL),
    CHECK (encryption_scheme = 'aes-256-gcm-envelope-v1'),
    CHECK (key_id <> ''),
    CHECK (dek_wrapped_by <> ''),
    CHECK (octet_length(encrypted_dek) > 0),
    CHECK (dek_wrapped_by <> 'local' OR octet_length(encrypted_dek) = 48),
    CHECK (dek_wrapped_by <> 'local' OR octet_length(encrypted_dek_nonce) = 12),
    CHECK (octet_length(nonce) = 12),
    CHECK (octet_length(ciphertext) > 16),
    CHECK (oauth_access_token_expires_at IS NULL OR oauth_access_token_expires_at > created_at),
    FOREIGN KEY (org_id, secret_id) REFERENCES secrets(org_id, id)
);

ALTER TABLE secrets ADD FOREIGN KEY (id, current_version_id)
    REFERENCES secret_versions(secret_id, id)
    DEFERRABLE INITIALLY DEFERRED;

CREATE INDEX secret_versions_key_id_idx
    ON secret_versions(key_id);

CREATE UNIQUE INDEX secret_versions_mcp_oauth_flow_id_idx
    ON secret_versions(mcp_oauth_flow_id)
    WHERE mcp_oauth_flow_id IS NOT NULL;

CREATE TABLE secret_oauth_refresh_leases (
    org_id uuid NOT NULL,
    secret_id uuid NOT NULL,
    owner_token uuid NOT NULL,
    expected_secret_version_id uuid NOT NULL,
    expires_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (org_id, secret_id),
    CHECK (expires_at > updated_at),
    FOREIGN KEY (org_id, secret_id) REFERENCES secrets(org_id, id) ON DELETE CASCADE,
    FOREIGN KEY (secret_id, expected_secret_version_id) REFERENCES secret_versions(secret_id, id) ON DELETE CASCADE
);

CREATE INDEX secret_oauth_leases_expected_version_idx
    ON secret_oauth_refresh_leases(secret_id, expected_secret_version_id);

CREATE TABLE secret_grants (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id uuid NOT NULL,
    secret_id uuid NOT NULL,
    target_project_id uuid NOT NULL,
    created_at timestamptz NOT NULL,
    UNIQUE (org_id, secret_id, target_project_id),
    FOREIGN KEY (org_id, secret_id) REFERENCES secrets(org_id, id),
    FOREIGN KEY (org_id, target_project_id) REFERENCES projects(org_id, id)
);

-- +goose StatementBegin
CREATE FUNCTION secret_grants_reject_project_self_grant() RETURNS trigger AS $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM secrets
        WHERE org_id = NEW.org_id
          AND id = NEW.secret_id
          AND owner_kind = 'project'
          AND owner_project_id = NEW.target_project_id
    ) THEN
        RAISE EXCEPTION 'secret is already available to its owner project'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER secret_grants_no_project_self_grant
    BEFORE INSERT OR UPDATE OF org_id, secret_id, target_project_id ON secret_grants
    FOR EACH ROW
    EXECUTE FUNCTION secret_grants_reject_project_self_grant();

CREATE INDEX secret_grants_target_project_secret_idx
    ON secret_grants(org_id, target_project_id, secret_id);

CREATE VIEW principal_project_authorization_roles AS
WITH active_projects AS (
    SELECT project.org_id, project.id
    FROM projects project
    JOIN orgs org ON org.id = project.org_id
    WHERE project.deleted_at IS NULL
      AND org.deleted_at IS NULL
)
SELECT om.org_id, p.id AS project_id, om.user_id, om.org_api_key_id, 'admin'::text AS role
FROM org_memberships om
JOIN active_projects p
    ON p.org_id = om.org_id
WHERE om.role IN ('owner', 'admin')
UNION
SELECT pm.org_id, pm.project_id, om.user_id, om.org_api_key_id, pm.role
FROM project_memberships pm
JOIN org_memberships om ON om.org_id = pm.org_id AND om.id = pm.org_membership_id
JOIN active_projects p ON p.org_id = pm.org_id AND p.id = pm.project_id;

-- An invitation row is a pending invite; accepting, declining, or revoking
-- consumes (hard deletes) it.
CREATE TABLE org_invitations (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id uuid NOT NULL REFERENCES orgs(id),
    email text NOT NULL,
    normalized_email text NOT NULL,
    org_role text NOT NULL,
    created_at timestamptz NOT NULL,
    UNIQUE (org_id, normalized_email),
    CHECK (email <> ''),
    CHECK (normalized_email <> ''),
    CHECK (org_role IN ('admin', 'member'))
);

CREATE INDEX org_invitations_org_created_idx
    ON org_invitations(org_id, created_at DESC, id DESC);

CREATE INDEX org_invitations_email_created_idx
    ON org_invitations(normalized_email, created_at ASC, id ASC);

CREATE TABLE browser_sessions (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    user_id uuid NOT NULL REFERENCES users(id),
    token_hash text NOT NULL,
    csrf_token_hash text NOT NULL,
    created_at timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    UNIQUE (token_hash),
    UNIQUE (user_id, id),
    CHECK (token_hash <> ''),
    CHECK (csrf_token_hash <> ''),
    CHECK (expires_at > created_at),
    CHECK (last_seen_at >= created_at),
    CHECK (revoked_at IS NULL OR revoked_at >= created_at)
);

CREATE INDEX browser_sessions_active_user_idx
    ON browser_sessions(user_id)
    WHERE revoked_at IS NULL;

CREATE INDEX browser_sessions_cleanup_idx
    ON browser_sessions(expires_at, id);

CREATE TABLE auth_device_flows (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    device_code_hash text NOT NULL,
    user_code_hash text NOT NULL,
    client_name text NOT NULL,
    token_name text NOT NULL,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    approved_by_user_id uuid,
    approved_browser_session_id uuid,
    approved_at timestamptz,
    denied_at timestamptz,
    consumed_at timestamptz,
    last_polled_at timestamptz,
    UNIQUE (device_code_hash),
    UNIQUE (user_code_hash),
    CHECK (device_code_hash <> ''),
    CHECK (user_code_hash <> ''),
    CHECK (char_length(client_name) BETWEEN 1 AND 128 AND client_name !~ '[[:cntrl:]]'),
    CHECK (char_length(token_name) BETWEEN 1 AND 128 AND token_name !~ '[[:cntrl:]]'),
    CHECK (expires_at > created_at),
    CHECK ((approved_at IS NULL AND approved_by_user_id IS NULL AND approved_browser_session_id IS NULL) OR (approved_at IS NOT NULL AND approved_by_user_id IS NOT NULL AND approved_browser_session_id IS NOT NULL)),
    CHECK (approved_at IS NULL OR (approved_at >= created_at AND approved_at < expires_at)),
    CHECK (denied_at IS NULL OR denied_at >= created_at),
    CHECK (consumed_at IS NULL OR consumed_at >= created_at),
    CHECK (consumed_at IS NULL OR approved_at IS NOT NULL),
    CHECK (consumed_at IS NULL OR denied_at IS NULL),
    FOREIGN KEY (approved_by_user_id, approved_browser_session_id) REFERENCES browser_sessions(user_id, id)
);

CREATE INDEX auth_device_flows_cleanup_idx
    ON auth_device_flows(expires_at, id);

CREATE INDEX auth_device_flows_approved_session_idx
    ON auth_device_flows(approved_browser_session_id, approved_by_user_id)
    WHERE approved_browser_session_id IS NOT NULL;
