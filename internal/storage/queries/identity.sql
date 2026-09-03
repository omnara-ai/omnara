-- name: GetInstallationID :one
SELECT id
FROM installation
WHERE singleton_key = 1;

-- name: CreateOrg :one
INSERT INTO orgs(id, name, idempotency_key, created_at, updated_at)
VALUES (sqlc.arg(id), sqlc.arg(name), sqlc.narg(idempotency_key), transaction_timestamp(), transaction_timestamp())
ON CONFLICT (idempotency_key) DO NOTHING
RETURNING id, name, coalesce(idempotency_key, '') AS idempotency_key, created_at, updated_at;

-- name: GetOrgByIdempotencyKey :one
SELECT id, name, coalesce(idempotency_key, '') AS idempotency_key, created_at, updated_at
FROM orgs
WHERE idempotency_key = sqlc.arg(idempotency_key)::text AND deleted_at IS NULL;

-- name: GetOrg :one
SELECT id, name, coalesce(idempotency_key, '') AS idempotency_key, created_at, updated_at
FROM orgs
WHERE id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: LockOrganizationLifecycleShared :one
SELECT id
FROM orgs
WHERE id = sqlc.arg(org_id)
  AND deleted_at IS NULL
FOR SHARE;

-- name: DeleteOrganization :execrows
UPDATE orgs SET deleted_at = transaction_timestamp(), updated_at = transaction_timestamp()
WHERE id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: DeleteOrganizationProjects :exec
UPDATE projects SET deleted_at = transaction_timestamp(), updated_at = transaction_timestamp()
WHERE org_id = sqlc.arg(org_id) AND deleted_at IS NULL;

-- name: DeleteOrganizationMemberships :exec
DELETE FROM org_memberships
WHERE org_id = sqlc.arg(org_id);

-- name: DeleteOrgInvitationsForOrgDeletion :exec
-- Invitations must not mint memberships in a deleted organization.
DELETE FROM org_invitations
WHERE org_id = sqlc.arg(org_id);

-- name: OrgExistsActive :one
SELECT EXISTS (
  SELECT 1 FROM orgs WHERE id = sqlc.arg(id) AND deleted_at IS NULL
) AS org_exists;

-- name: DeleteOrganizationConfiguredModels :exec
UPDATE configured_models SET deleted_at = transaction_timestamp(), updated_at = transaction_timestamp()
WHERE org_id = sqlc.arg(org_id) AND deleted_at IS NULL;

-- name: DeleteOrganizationModelProviderConfigs :exec
-- Clearing the credential releases the secret for the deletion below.
UPDATE model_provider_configs
SET deleted_at = transaction_timestamp(), credential_secret_id = NULL, updated_at = transaction_timestamp()
WHERE org_id = sqlc.arg(org_id) AND deleted_at IS NULL;

-- name: DeleteOrganizationSkillGrants :exec
DELETE FROM skill_grants
WHERE org_id = sqlc.arg(org_id);

-- name: DeleteOrganizationSecrets :exec
-- Soft deletes every org secret and clears the version pointers; version rows
-- (the ciphertext) are destroyed separately once nothing references them.
UPDATE secrets
SET deleted_at = transaction_timestamp(),
    current_version_id = NULL,
    updated_at = transaction_timestamp()
WHERE org_id = sqlc.arg(org_id) AND deleted_at IS NULL;

-- name: DeleteOrganizationSecretGrants :exec
DELETE FROM secret_grants
WHERE org_id = sqlc.arg(org_id);

-- name: DeleteOrganizationSecretOAuthLeases :exec
DELETE FROM secret_oauth_refresh_leases
WHERE org_id = sqlc.arg(org_id);

-- name: DestroyUnreferencedSecretVersionsForDeletedOrg :execrows
-- The single owner of the "what references a secret" predicate for deleted
-- organizations. Organization deletion soft deletes its secrets and calls this
-- in the same transaction to destroy their ciphertext; pool-machine teardown
-- completion re-runs it inline for pool credentials it still needed at
-- deletion time.
DELETE FROM secret_versions version
USING secrets secret
JOIN orgs org ON org.id = secret.org_id
WHERE secret.org_id = version.org_id
  AND secret.id = version.secret_id
  AND org.id = sqlc.arg(org_id)
  AND org.deleted_at IS NOT NULL
  AND secret.deleted_at IS NOT NULL
  AND NOT EXISTS (
    SELECT 1 FROM model_provider_configs config
    WHERE config.org_id = secret.org_id AND config.credential_secret_id = secret.id
  )
  AND NOT EXISTS (
    SELECT 1 FROM machine_pools pool
    WHERE pool.org_id = secret.org_id AND pool.provider_auth_secret_id = secret.id
  )
  AND NOT EXISTS (
    SELECT 1 FROM integration_installs install
    WHERE install.org_id = secret.org_id AND install.credential_secret_id = secret.id
  )
  AND NOT EXISTS (
    SELECT 1 FROM integration_apps app
    WHERE app.org_id = secret.org_id AND app.credential_secret_id = secret.id
  );

-- name: ListActiveProjectIDsForOrganization :many
SELECT id FROM projects WHERE org_id = sqlc.arg(org_id) AND deleted_at IS NULL ORDER BY id;

-- name: ListActiveAgentIDsForProjectDeletion :many
SELECT id FROM agents
WHERE project_id = sqlc.arg(project_id) AND state = 'active'
ORDER BY id;

-- name: ProjectHasActiveAgentsForDeletion :one
SELECT EXISTS (
  SELECT 1 FROM agents
  WHERE project_id = sqlc.arg(project_id) AND state = 'active'
) AS has_active_agents;

-- name: GetOrgCreationReplayForUser :one
SELECT
    org.id AS org_id,
    org.name AS org_name,
    coalesce(org.idempotency_key, '') AS org_idempotency_key,
    org.created_at AS org_created_at,
    org.updated_at AS org_updated_at,
    membership.role AS membership_role,
    membership.created_at AS membership_created_at,
    project.id AS project_id,
    project.name AS project_name,
    coalesce(project.idempotency_key, '') AS project_idempotency_key,
    project.created_at AS project_created_at,
    project.updated_at AS project_updated_at
FROM orgs org
JOIN org_memberships membership
  ON membership.org_id = org.id
 AND membership.user_id = sqlc.arg(user_id)::uuid
JOIN projects project
  ON project.org_id = org.id
 AND project.idempotency_key = 'default'
 AND project.deleted_at IS NULL
WHERE org.idempotency_key = sqlc.arg(idempotency_key)::text
  AND org.deleted_at IS NULL;

-- name: CreateProject :one
INSERT INTO projects(org_id, name, idempotency_key, created_at, updated_at)
VALUES (sqlc.arg(org_id), sqlc.arg(name), sqlc.narg(idempotency_key), transaction_timestamp(), transaction_timestamp())
ON CONFLICT (org_id, idempotency_key) DO NOTHING
RETURNING id, org_id, name, coalesce(idempotency_key, '') AS idempotency_key, created_at, updated_at;

-- name: GetProjectByIdempotencyKey :one
SELECT id, org_id, name, coalesce(idempotency_key, '') AS idempotency_key, created_at, updated_at
FROM projects
WHERE org_id = sqlc.arg(org_id) AND idempotency_key = sqlc.arg(idempotency_key)::text AND deleted_at IS NULL;

-- name: GetProject :one
SELECT id, org_id, name, coalesce(idempotency_key, '') AS idempotency_key, created_at, updated_at
FROM projects
WHERE org_id = $1 AND id = $2 AND deleted_at IS NULL;

-- name: DeleteProject :execrows
UPDATE projects SET deleted_at = transaction_timestamp(), updated_at = transaction_timestamp()
WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: DeleteProjectMemberships :exec
DELETE FROM project_memberships
WHERE org_id = sqlc.arg(org_id) AND project_id = sqlc.arg(project_id);

-- name: DeleteProjectMachineGrantsForProjectDeletion :exec
DELETE FROM project_machine_grants
WHERE org_id = sqlc.arg(org_id) AND project_id = sqlc.arg(project_id);

-- name: DeleteProjectMachinePoolGrantsForProjectDeletion :exec
DELETE FROM project_machine_pool_grants
WHERE org_id = sqlc.arg(org_id) AND project_id = sqlc.arg(project_id);

-- name: DeleteProjectModelGrantsForProjectDeletion :exec
DELETE FROM project_model_grants
WHERE org_id = sqlc.arg(org_id) AND project_id = sqlc.arg(project_id);

-- name: DeleteProjectAgentProfileVersions :exec
UPDATE agent_profile_versions SET deleted_at = transaction_timestamp()
WHERE project_id = sqlc.arg(project_id) AND deleted_at IS NULL;

-- name: DeleteProjectAgentProfiles :exec
UPDATE agent_profiles SET deleted_at = transaction_timestamp(), updated_at = transaction_timestamp()
WHERE project_id = sqlc.arg(project_id) AND deleted_at IS NULL;

-- name: DeleteProjectCronTriggers :exec
UPDATE cron_triggers SET deleted_at = transaction_timestamp(), updated_at = transaction_timestamp()
WHERE project_id = sqlc.arg(project_id) AND deleted_at IS NULL;

-- name: DeleteProjectIntegrationTargets :exec
UPDATE integration_targets SET deleted_at = transaction_timestamp(), updated_at = transaction_timestamp()
WHERE project_id = sqlc.arg(project_id) AND deleted_at IS NULL;

-- name: DeleteProjectIntegrationRoutes :exec
UPDATE integration_routes
SET state = 'disabled', deleted_at = transaction_timestamp(), updated_at = transaction_timestamp()
WHERE project_id = sqlc.arg(project_id) AND deleted_at IS NULL;

-- name: RevokeProjectIntegrationTargetBindings :exec
UPDATE integration_target_bindings
SET revoked_at = transaction_timestamp(),
    updated_at = transaction_timestamp()
WHERE project_id = sqlc.arg(project_id)
  AND revoked_at IS NULL;

-- name: DeleteProjectIntegrationApps :exec
-- Only registrations restricted to this project are project-owned. Shared
-- organization registrations survive project deletion.
UPDATE integration_apps
SET credential_secret_id = NULL,
    state = 'disabled',
    deleted_at = transaction_timestamp(),
    updated_at = transaction_timestamp()
WHERE org_id = sqlc.arg(org_id)
  AND owner_project_id = sqlc.arg(project_id)::uuid
  AND deleted_at IS NULL;

-- name: DeleteProjectIntegrationRuntimeUnits :exec
UPDATE integration_runtime_units
SET desired_state = 'stopped',
    status = 'stopped',
    lease_owner = NULL,
    lease_token = NULL,
    leased_at = NULL,
    renewed_at = NULL,
    lease_expires_at = NULL,
    lease_spec_revision = NULL,
    lease_app_configuration_revision = NULL,
    lease_install_configuration_revision = NULL,
    deleted_at = transaction_timestamp(),
    updated_at = transaction_timestamp()
WHERE integration_runtime_units.org_id = sqlc.arg(org_id)
  AND (
    integration_runtime_units.project_id = sqlc.arg(project_id)::uuid
    OR (
      integration_runtime_units.project_id IS NULL
      AND EXISTS (
        SELECT 1
        FROM integration_apps app
        WHERE app.id = integration_runtime_units.integration_app_id
          AND app.org_id = sqlc.arg(org_id)
          AND app.owner_project_id = sqlc.arg(project_id)::uuid
      )
    )
  )
  AND integration_runtime_units.deleted_at IS NULL;

-- name: DeleteOrganizationIntegrationApps :exec
UPDATE integration_apps
SET credential_secret_id = NULL,
    state = 'disabled',
    deleted_at = transaction_timestamp(),
    updated_at = transaction_timestamp()
WHERE org_id = sqlc.arg(org_id) AND deleted_at IS NULL;

-- name: DeleteOrganizationIntegrationRuntimeUnits :exec
UPDATE integration_runtime_units
SET desired_state = 'stopped',
    status = 'stopped',
    lease_owner = NULL,
    lease_token = NULL,
    leased_at = NULL,
    renewed_at = NULL,
    lease_expires_at = NULL,
    lease_spec_revision = NULL,
    lease_app_configuration_revision = NULL,
    lease_install_configuration_revision = NULL,
    deleted_at = transaction_timestamp(),
    updated_at = transaction_timestamp()
WHERE org_id = sqlc.arg(org_id) AND deleted_at IS NULL;

-- name: DeleteProjectIntegrationInstalls :exec
-- Clearing the credential releases the secret for the deletion below.
UPDATE integration_installs
SET credential_secret_id = NULL, deleted_at = transaction_timestamp(), updated_at = transaction_timestamp()
WHERE org_id = sqlc.arg(org_id) AND project_id = sqlc.arg(project_id) AND deleted_at IS NULL;

-- name: DeleteSkillRevisionsForOwner :exec
-- NULL owner_project_id means every skill in the organization.
UPDATE skill_revisions revision SET deleted_at = transaction_timestamp()
FROM skills skill
WHERE skill.org_id = sqlc.arg(org_id)
  AND (sqlc.narg(owner_project_id)::uuid IS NULL OR skill.owner_project_id = sqlc.narg(owner_project_id)::uuid)
  AND revision.skill_id = skill.id AND revision.deleted_at IS NULL;

-- name: DeleteProjectSkillGrants :exec
-- @sqlc-vet-disable skills-deleted-at
-- Project purge drops grants of soft-deleted skills too.
DELETE FROM skill_grants grant_row
WHERE grant_row.org_id = sqlc.arg(org_id)
  AND (grant_row.target_project_id = sqlc.arg(project_id) OR grant_row.skill_id IN (
    SELECT skill.id FROM skills skill WHERE skill.org_id = sqlc.arg(org_id) AND skill.owner_project_id = sqlc.arg(project_id)
  ));

-- name: DeleteSkillsForOwner :exec
-- NULL owner_project_id means every skill in the organization.
UPDATE skills SET deleted_at = transaction_timestamp()
WHERE org_id = sqlc.arg(org_id)
  AND (sqlc.narg(owner_project_id)::uuid IS NULL OR owner_project_id = sqlc.narg(owner_project_id)::uuid)
  AND deleted_at IS NULL;

-- name: ListSkillRevisionKeysForOwner :many
-- @sqlc-vet-disable skills-deleted-at skill-revisions-deleted-at
-- Archive purge enumerates soft-deleted skills and revisions too.
-- NULL owner_project_id means every skill in the organization.
SELECT revision.skill_id, revision.id
FROM skill_revisions revision
JOIN skills skill ON skill.id = revision.skill_id
WHERE skill.org_id = sqlc.arg(org_id)
  AND (sqlc.narg(owner_project_id)::uuid IS NULL OR skill.owner_project_id = sqlc.narg(owner_project_id)::uuid)
ORDER BY revision.skill_id, revision.id;

-- name: DeleteProjectSecretGrants :exec
-- Removes grants from project-owned secrets outward and from other secrets
-- into the project.
DELETE FROM secret_grants grant_row
USING secrets secret
WHERE grant_row.org_id = sqlc.arg(org_id)
  AND grant_row.secret_id = secret.id
  AND (grant_row.target_project_id = sqlc.arg(project_id) OR secret.owner_project_id = sqlc.arg(project_id));

-- name: DeleteProjectSecretVersions :exec
DELETE FROM secret_versions version
USING secrets secret
WHERE secret.org_id = sqlc.arg(org_id) AND secret.owner_project_id = sqlc.arg(project_id)
  AND version.org_id = secret.org_id AND version.secret_id = secret.id;

-- name: DeleteProjectSecrets :exec
UPDATE secrets
SET deleted_at = transaction_timestamp(),
    current_version_id = NULL,
    updated_at = transaction_timestamp()
WHERE org_id = sqlc.arg(org_id) AND owner_project_id = sqlc.arg(project_id) AND deleted_at IS NULL;

-- name: ProjectOwnedSecretsReferenced :one
SELECT EXISTS (
  SELECT 1 FROM secrets secret
  WHERE secret.org_id = sqlc.arg(org_id)
    AND secret.owner_project_id = sqlc.arg(project_id)
    AND secret.deleted_at IS NULL
    AND (
      EXISTS (SELECT 1 FROM model_provider_configs config WHERE config.org_id = secret.org_id AND config.credential_secret_id = secret.id)
      OR EXISTS (SELECT 1 FROM machine_pools pool WHERE pool.org_id = secret.org_id AND pool.provider_auth_secret_id = secret.id)
      OR EXISTS (SELECT 1 FROM integration_installs install WHERE install.org_id = secret.org_id AND install.credential_secret_id = secret.id)
      OR EXISTS (SELECT 1 FROM integration_apps app WHERE app.org_id = secret.org_id AND app.credential_secret_id = secret.id)
    )
) AS is_referenced;

-- name: ListVisibleProjectRolesForPrincipal :many
WITH visible_projects AS (
  SELECT DISTINCT p.id, p.org_id, p.name, coalesce(p.idempotency_key, '') AS idempotency_key, p.created_at, p.updated_at
  FROM principal_project_authorization_roles roles
  JOIN projects p
    ON p.org_id = roles.org_id
   AND p.id = roles.project_id
  WHERE roles.org_id = sqlc.arg(org_id)
    AND (
      (sqlc.narg(user_id)::uuid IS NOT NULL AND roles.user_id = sqlc.narg(user_id)::uuid)
      OR (sqlc.narg(org_api_key_id)::uuid IS NOT NULL AND roles.org_api_key_id = sqlc.narg(org_api_key_id)::uuid)
    )
    AND p.deleted_at IS NULL
    AND (
      sqlc.narg(cursor_created_at)::timestamptz IS NULL
      OR (p.created_at, p.id) < (sqlc.narg(cursor_created_at)::timestamptz, sqlc.narg(cursor_id)::uuid)
    )
  ORDER BY p.created_at DESC, p.id DESC
  LIMIT sqlc.arg(row_limit)::bigint
)
SELECT p.id, p.org_id, p.name, p.idempotency_key, p.created_at, p.updated_at, roles.role
FROM visible_projects p
JOIN principal_project_authorization_roles roles
  ON roles.org_id = p.org_id
 AND roles.project_id = p.id
 AND (
   (sqlc.narg(user_id)::uuid IS NOT NULL AND roles.user_id = sqlc.narg(user_id)::uuid)
   OR (sqlc.narg(org_api_key_id)::uuid IS NOT NULL AND roles.org_api_key_id = sqlc.narg(org_api_key_id)::uuid)
 )
ORDER BY p.created_at DESC, p.id DESC, roles.role;

-- name: CreateUser :one
INSERT INTO users(display_name, created_at, updated_at)
VALUES (sqlc.arg(display_name), transaction_timestamp(), transaction_timestamp())
RETURNING id, display_name, deleted_at, created_at, updated_at;

-- name: UpdateUserDisplayName :one
UPDATE users
SET display_name = sqlc.arg(display_name),
    updated_at = transaction_timestamp()
WHERE id = sqlc.arg(id)
RETURNING id, display_name, deleted_at, created_at, updated_at;

-- name: GetUser :one
SELECT id, display_name, deleted_at, created_at, updated_at
FROM users
WHERE id = $1 AND deleted_at IS NULL;

-- name: DeleteUser :execrows
UPDATE users SET deleted_at = transaction_timestamp(), updated_at = transaction_timestamp()
WHERE id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: DeleteUserOrgMemberships :exec
-- Project memberships hang off the org membership row and delete with it.
DELETE FROM org_memberships
WHERE user_id = sqlc.arg(user_id)::uuid;

-- name: DeleteUserAuthTokensForAccountDeletion :exec
DELETE FROM user_auth_tokens
WHERE user_id = sqlc.arg(user_id);

-- name: DeleteUserEmailsForAccountDeletion :exec
DELETE FROM user_emails
WHERE user_id = sqlc.arg(user_id);

-- name: DeleteUserAuthIdentitiesForAccountDeletion :exec
DELETE FROM user_auth_identities
WHERE user_id = sqlc.arg(user_id);

-- name: DeleteUserCredentialsForAccountDeletion :exec
DELETE FROM user_credentials
WHERE user_id = sqlc.arg(user_id);

-- name: DeleteUserOwnedSecretsForUser :exec
UPDATE secrets
SET deleted_at = transaction_timestamp(),
    current_version_id = NULL,
    updated_at = transaction_timestamp()
WHERE owner_user_id = sqlc.arg(user_id)::uuid AND deleted_at IS NULL;

-- name: DeleteUserOwnedSecretVersionsForUser :exec
DELETE FROM secret_versions version
USING secrets secret
WHERE secret.owner_user_id = sqlc.arg(user_id)::uuid
  AND version.org_id = secret.org_id AND version.secret_id = secret.id;

-- name: DeleteUserOwnedSecretChildrenForUser :exec
DELETE FROM secret_grants grant_row
USING secrets secret
WHERE secret.owner_user_id = sqlc.arg(user_id)::uuid
  AND grant_row.org_id = secret.org_id AND grant_row.secret_id = secret.id;

-- name: UserOwnedSecretsReferenced :one
SELECT EXISTS (
  SELECT 1 FROM secrets secret
  WHERE secret.owner_user_id = sqlc.arg(user_id)::uuid
    AND secret.deleted_at IS NULL
    AND (
      EXISTS (SELECT 1 FROM model_provider_configs config WHERE config.org_id = secret.org_id AND config.credential_secret_id = secret.id)
      OR EXISTS (SELECT 1 FROM machine_pools pool WHERE pool.org_id = secret.org_id AND pool.provider_auth_secret_id = secret.id)
      OR EXISTS (SELECT 1 FROM integration_installs install WHERE install.org_id = secret.org_id AND install.credential_secret_id = secret.id)
    )
) AS is_referenced;

-- name: ListUserOwnedSkillsForUser :many
SELECT id, org_id
FROM skills
WHERE owner_user_id = sqlc.arg(user_id)::uuid AND deleted_at IS NULL
ORDER BY id;

-- name: ListUserOwnedSkillIDsForOrg :many
SELECT id
FROM skills
WHERE org_id = sqlc.arg(org_id) AND owner_user_id = sqlc.arg(user_id)::uuid AND deleted_at IS NULL
ORDER BY id;

-- name: UserIsLastOwnerOfAnyOrg :one
SELECT EXISTS (
  SELECT 1 FROM org_memberships membership
  JOIN orgs org ON org.id = membership.org_id AND org.deleted_at IS NULL
  WHERE membership.user_id = sqlc.arg(user_id)::uuid
    AND membership.role = 'owner'
    AND NOT EXISTS (
      SELECT 1 FROM org_memberships other
      WHERE other.org_id = membership.org_id
        AND other.role = 'owner'
        AND other.user_id IS NOT NULL
        AND other.user_id <> membership.user_id
    )
) AS is_last_owner;

-- name: LockUserForUpdate :one
SELECT id
FROM users
WHERE id = sqlc.arg(id) AND deleted_at IS NULL
FOR UPDATE;

-- name: CreateUserEmail :one
INSERT INTO user_emails(user_id, email, normalized_email, verified_at, is_primary, created_at, updated_at)
VALUES (
  sqlc.arg(user_id),
  sqlc.arg(email),
  sqlc.arg(normalized_email),
  CASE WHEN sqlc.arg(verified)::boolean THEN transaction_timestamp() END,
  sqlc.arg(is_primary),
  transaction_timestamp(),
  transaction_timestamp()
)
RETURNING id, user_id, email, normalized_email, verified_at, is_primary, created_at, updated_at;

-- name: GetVerifiedUserEmailByNormalizedEmail :one
SELECT id, user_id, email, normalized_email, verified_at, is_primary, created_at, updated_at
FROM user_emails
WHERE normalized_email = sqlc.arg(normalized_email) AND verified_at IS NOT NULL;

-- name: LockUserEmailsByNormalizedEmail :many
SELECT id
FROM user_emails
WHERE normalized_email = sqlc.arg(normalized_email)
ORDER BY id
FOR UPDATE;

-- name: LockNormalizedEmailKey :exec
SELECT pg_advisory_xact_lock(hashtext(sqlc.arg(normalized_email)::text));

-- name: VerifyUserEmail :one
UPDATE user_emails
SET verified_at = transaction_timestamp(),
    updated_at = transaction_timestamp()
WHERE id = sqlc.arg(id)
  AND user_id = sqlc.arg(user_id)
  AND verified_at IS NULL
RETURNING id, user_id, email, normalized_email, verified_at, is_primary, created_at, updated_at;

-- name: ListVerifiedUserEmailsByUser :many
SELECT id, user_id, email, normalized_email, verified_at, is_primary, created_at, updated_at
FROM user_emails
WHERE user_id = sqlc.arg(user_id) AND verified_at IS NOT NULL
ORDER BY is_primary DESC, created_at ASC;

-- name: PrimaryVerifiedEmailsForUsers :many
SELECT user_id, email
FROM user_emails
WHERE user_id = ANY(sqlc.arg(user_ids)::uuid[])
  AND verified_at IS NOT NULL
  AND is_primary;

-- name: CreateUserCredential :one
INSERT INTO user_credentials(user_id, password_hash, password_changed_at, created_at, updated_at)
VALUES (sqlc.arg(user_id), sqlc.arg(password_hash), transaction_timestamp(), transaction_timestamp(), transaction_timestamp())
RETURNING user_id, password_hash, password_changed_at, created_at, updated_at;

-- name: UpdateUserCredentialPasswordHash :one
UPDATE user_credentials
SET password_hash = sqlc.arg(password_hash),
    password_changed_at = statement_timestamp(),
    updated_at = statement_timestamp()
WHERE user_id = sqlc.arg(user_id)
RETURNING user_id, password_hash, password_changed_at, created_at, updated_at;

-- name: RehashUserCredentialPasswordHash :exec
UPDATE user_credentials
SET password_hash = sqlc.arg(password_hash),
    updated_at = transaction_timestamp()
WHERE user_id = sqlc.arg(user_id)
  AND password_hash = sqlc.arg(previous_password_hash);

-- name: GetPasswordLoginByVerifiedEmail :one
SELECT users.id AS user_id,
       users.display_name,
       users.created_at AS user_created_at,
       users.updated_at AS user_updated_at,
       user_emails.id AS user_email_id,
       user_emails.email,
       user_emails.normalized_email,
       user_emails.verified_at,
       user_credentials.password_hash
FROM user_emails
JOIN users ON users.id = user_emails.user_id
JOIN user_credentials ON user_credentials.user_id = users.id
WHERE user_emails.normalized_email = sqlc.arg(normalized_email)
  AND user_emails.verified_at IS NOT NULL
  AND users.deleted_at IS NULL
LIMIT 1;

-- name: GetPasswordCredentialByUserForUpdate :one
SELECT users.id AS user_id,
       users.display_name,
       users.created_at AS user_created_at,
       users.updated_at AS user_updated_at,
       user_credentials.password_hash
FROM users
JOIN user_credentials ON user_credentials.user_id = users.id
WHERE users.id = sqlc.arg(user_id)
  AND users.deleted_at IS NULL
FOR UPDATE OF users, user_credentials;

-- name: CreateUserAuthToken :one
INSERT INTO user_auth_tokens(user_id, user_email_id, purpose, token_hash, created_at, expires_at)
VALUES (
  sqlc.arg(user_id),
  sqlc.narg(user_email_id),
  sqlc.arg(purpose),
  sqlc.arg(token_hash),
  statement_timestamp(),
  statement_timestamp() + (sqlc.arg(ttl_seconds)::bigint * interval '1 second')
)
RETURNING id, user_id, user_email_id, purpose, token_hash, created_at, expires_at, consumed_at;

-- name: GetActiveUserAuthTokenByHashForUpdate :one
SELECT token.id,
       token.user_id,
       token.user_email_id,
       token.purpose,
       token.token_hash,
       token.created_at,
       token.expires_at,
       token.consumed_at,
       email.email,
       email.normalized_email
FROM user_auth_tokens token
LEFT JOIN user_emails email ON email.id = token.user_email_id
WHERE token.token_hash = sqlc.arg(token_hash)
  AND token.purpose = sqlc.arg(purpose)
  AND token.consumed_at IS NULL
  AND token.expires_at > statement_timestamp()
FOR UPDATE OF token;

-- name: GetActiveUserAuthTokenByHash :one
SELECT token.id,
       token.user_id,
       token.user_email_id,
       token.purpose,
       token.token_hash,
       token.created_at,
       token.expires_at,
       token.consumed_at,
       email.email,
       email.normalized_email
FROM user_auth_tokens token
LEFT JOIN user_emails email ON email.id = token.user_email_id
WHERE token.token_hash = sqlc.arg(token_hash)
  AND token.purpose = sqlc.arg(purpose)
  AND token.consumed_at IS NULL
  AND token.expires_at > statement_timestamp();

-- name: ConsumeUserAuthToken :execrows
UPDATE user_auth_tokens
SET consumed_at = statement_timestamp()
WHERE id = sqlc.arg(id)
  AND consumed_at IS NULL
  AND expires_at > statement_timestamp();

-- name: ConsumeUnconsumedUserAuthTokensForUserPurpose :exec
UPDATE user_auth_tokens
SET consumed_at = statement_timestamp()
WHERE user_id = sqlc.arg(user_id)
  AND purpose = sqlc.arg(purpose)
  AND consumed_at IS NULL;

-- name: ConsumeUnconsumedUserAuthTokensForUserPurposeExcept :exec
UPDATE user_auth_tokens
SET consumed_at = statement_timestamp()
WHERE user_id = sqlc.arg(user_id)
  AND purpose = sqlc.arg(purpose)
  AND id <> sqlc.arg(excluded_id)
  AND consumed_at IS NULL;

-- name: InsertAuthConnector :one
INSERT INTO auth_connectors(id, slug, kind, display_name, issuer, authorization_url, token_url, userinfo_url, client_id, encrypted_client_secret, scopes, email_trust_policy, enabled, created_at, updated_at)
VALUES (sqlc.arg(id), sqlc.arg(slug), sqlc.arg(kind), sqlc.arg(display_name), sqlc.arg(issuer), sqlc.arg(authorization_url), sqlc.arg(token_url), sqlc.arg(userinfo_url), sqlc.arg(client_id), sqlc.arg(encrypted_client_secret), sqlc.arg(scopes), sqlc.arg(email_trust_policy), sqlc.arg(enabled), transaction_timestamp(), transaction_timestamp())
ON CONFLICT DO NOTHING
RETURNING id, slug, kind, display_name, issuer, authorization_url, token_url, userinfo_url, client_id, encrypted_client_secret, scopes, email_trust_policy, enabled, created_at, updated_at;

-- name: GetAuthConnectorBySlugForUpdate :one
SELECT id, slug, kind, display_name, issuer, authorization_url, token_url, userinfo_url, client_id, encrypted_client_secret, scopes, email_trust_policy, enabled, created_at, updated_at
FROM auth_connectors
WHERE slug = sqlc.arg(slug)
FOR UPDATE;

-- name: GetAuthConnectorByIssuerForUpdate :one
SELECT id, slug, kind, display_name, issuer, authorization_url, token_url, userinfo_url, client_id, encrypted_client_secret, scopes, email_trust_policy, enabled, created_at, updated_at
FROM auth_connectors
WHERE issuer = sqlc.arg(issuer)
FOR UPDATE;

-- name: UpdateAuthConnectorConfig :one
UPDATE auth_connectors
SET display_name = sqlc.arg(display_name),
    authorization_url = sqlc.arg(authorization_url),
    token_url = sqlc.arg(token_url),
    userinfo_url = sqlc.arg(userinfo_url),
    client_id = sqlc.arg(client_id),
    encrypted_client_secret = sqlc.arg(encrypted_client_secret),
    scopes = sqlc.arg(scopes),
    email_trust_policy = sqlc.arg(email_trust_policy),
    enabled = sqlc.arg(enabled),
    updated_at = transaction_timestamp()
WHERE id = sqlc.arg(id)
RETURNING id, slug, kind, display_name, issuer, authorization_url, token_url, userinfo_url, client_id, encrypted_client_secret, scopes, email_trust_policy, enabled, created_at, updated_at;

-- name: GetEnabledAuthConnectorBySlug :one
SELECT id, slug, kind, display_name, issuer, authorization_url, token_url, userinfo_url, client_id, encrypted_client_secret, scopes, email_trust_policy, enabled, created_at, updated_at
FROM auth_connectors
WHERE slug = sqlc.arg(slug)
  AND enabled;

-- name: GetAuthConnectorEmailTrustPolicy :one
SELECT email_trust_policy
FROM auth_connectors
WHERE id = sqlc.arg(id);

-- name: ListEnabledAuthConnectorSummaries :many
SELECT id, slug, kind, display_name
FROM auth_connectors
WHERE enabled
ORDER BY slug;

-- name: DisableUnlistedAuthConnectors :execrows
UPDATE auth_connectors
SET enabled = false,
    updated_at = transaction_timestamp()
WHERE enabled
  AND NOT (slug = ANY(sqlc.arg(active_slugs)::text[]));

-- name: DeleteInactiveUserAuthTokens :execrows
WITH candidates AS (
    SELECT id
    FROM user_auth_tokens
    WHERE consumed_at IS NOT NULL
       OR expires_at <= transaction_timestamp()
    ORDER BY expires_at, id
    LIMIT sqlc.arg(limit_count)
)
DELETE FROM user_auth_tokens token
USING candidates
WHERE token.id = candidates.id;

-- name: DeleteInactiveBrowserSessions :execrows
WITH candidates AS (
    SELECT id
    FROM browser_sessions
    WHERE (revoked_at IS NOT NULL OR expires_at <= transaction_timestamp())
      AND NOT EXISTS (
          SELECT 1
          FROM auth_device_flows flow
          WHERE flow.approved_browser_session_id = browser_sessions.id
      )
    ORDER BY expires_at, id
    LIMIT sqlc.arg(limit_count)
)
DELETE FROM browser_sessions session
USING candidates
WHERE session.id = candidates.id;

-- name: DeleteAbandonedUnverifiedSignupUsers :execrows
-- @sqlc-vet-disable users-deleted-at
-- Hard-delete purge: abandonment criteria are independent of soft deletion.
WITH candidates AS MATERIALIZED (
    SELECT users.id
    FROM users
    WHERE users.created_at < transaction_timestamp() - (sqlc.arg(minimum_age_seconds)::bigint * interval '1 second')
      AND EXISTS (
          SELECT 1
          FROM user_emails email
          WHERE email.user_id = users.id
            AND email.verified_at IS NULL
      )
      AND NOT EXISTS (
          SELECT 1
          FROM user_emails email
          WHERE email.user_id = users.id
            AND email.verified_at IS NOT NULL
      )
      AND NOT EXISTS (
          SELECT 1
          FROM user_auth_tokens token
          WHERE token.user_id = users.id
            AND token.consumed_at IS NULL
            AND token.expires_at > transaction_timestamp()
      )
      AND NOT EXISTS (
          SELECT 1
          FROM user_credentials credential
          WHERE credential.user_id = users.id
      )
      AND NOT EXISTS (
          SELECT 1
          FROM user_auth_identities identity
          WHERE identity.user_id = users.id
      )
      AND NOT EXISTS (
          SELECT 1
          FROM personal_access_tokens token
          WHERE token.user_id = users.id
      )
      AND NOT EXISTS (
          SELECT 1
          FROM browser_sessions session
          WHERE session.user_id = users.id
      )
      AND NOT EXISTS (
          SELECT 1
          FROM org_memberships membership
          WHERE membership.user_id = users.id
      )
      AND NOT EXISTS (
          SELECT 1
          FROM default_model_provider_provisioning_jobs job
          WHERE job.creator_user_id = users.id
      )
    ORDER BY users.created_at, users.id
    LIMIT sqlc.arg(limit_count)
),
deleted_tokens AS (
    DELETE FROM user_auth_tokens token
    USING candidates
    WHERE token.user_id = candidates.id
    RETURNING token.id
),
deleted_emails AS (
    DELETE FROM user_emails email
    USING candidates
    WHERE email.user_id = candidates.id
      AND email.verified_at IS NULL
    RETURNING email.id
)
DELETE FROM users
USING candidates
WHERE users.id = candidates.id;

-- name: CreateUserAuthIdentity :one
INSERT INTO user_auth_identities(user_id, auth_connector_id, issuer, subject, email_at_link, email_verified, created_at)
VALUES (sqlc.arg(user_id), sqlc.arg(auth_connector_id), sqlc.arg(issuer), sqlc.arg(subject), sqlc.arg(email_at_link), sqlc.arg(email_verified), transaction_timestamp())
RETURNING id, user_id, auth_connector_id, issuer, subject, email_at_link, email_verified, created_at;

-- name: GetUserAuthIdentity :one
SELECT id, user_id, auth_connector_id, issuer, subject, email_at_link, email_verified, created_at
FROM user_auth_identities
WHERE auth_connector_id = sqlc.arg(auth_connector_id) AND subject = sqlc.arg(subject);

-- name: AddProjectMembership :one
INSERT INTO project_memberships(org_id, project_id, org_membership_id, role, created_at)
VALUES (sqlc.arg(org_id), sqlc.arg(project_id), sqlc.arg(org_membership_id), sqlc.arg(role), transaction_timestamp())
ON CONFLICT (project_id, org_membership_id)
DO UPDATE SET role = excluded.role
RETURNING org_id, project_id, org_membership_id, role, created_at;

-- name: AddUserOrgMembership :one
INSERT INTO org_memberships(org_id, user_id, role, created_at)
VALUES (sqlc.arg(org_id), sqlc.arg(user_id)::uuid, sqlc.arg(role), transaction_timestamp())
ON CONFLICT (org_id, user_id) WHERE user_id IS NOT NULL
DO UPDATE SET role = excluded.role
RETURNING id, org_id, user_id, role, created_at;

-- name: AddOrgAPIKeyOrgMembership :one
INSERT INTO org_memberships(org_id, org_api_key_id, role, created_at)
VALUES (sqlc.arg(org_id), sqlc.arg(org_api_key_id)::uuid, sqlc.arg(role), transaction_timestamp())
ON CONFLICT (org_id, org_api_key_id) WHERE org_api_key_id IS NOT NULL
DO UPDATE SET role = excluded.role
RETURNING id, org_id, org_api_key_id, role, created_at;

-- name: UpdateOrgMembershipRoleForUser :one
UPDATE org_memberships
SET role = sqlc.arg(role)
WHERE org_id = sqlc.arg(org_id) AND user_id = sqlc.arg(user_id)::uuid
RETURNING id, org_id, user_id, role, created_at;

-- name: UpdateOrgMembershipRoleForOrgAPIKey :one
UPDATE org_memberships
SET role = sqlc.arg(role)
WHERE org_id = sqlc.arg(org_id) AND org_api_key_id = sqlc.arg(org_api_key_id)::uuid
RETURNING id, org_id, org_api_key_id, role, created_at;

-- name: RemoveUserOrgMembership :one
DELETE FROM org_memberships
WHERE org_id = sqlc.arg(org_id) AND user_id = sqlc.arg(user_id)::uuid
RETURNING id, org_id, user_id, role, created_at;

-- name: RemoveOrgAPIKeyOrgMembership :one
DELETE FROM org_memberships
WHERE org_id = sqlc.arg(org_id) AND org_api_key_id = sqlc.arg(org_api_key_id)::uuid
RETURNING id, org_id, org_api_key_id, role, created_at;

-- name: DeleteUserOwnedSecretsForOrgMember :exec
-- A removed member's personal secrets leave with them: rows soft-deleted,
-- versions destroyed by the companion queries.
UPDATE secrets
SET deleted_at = transaction_timestamp(),
    current_version_id = NULL,
    updated_at = transaction_timestamp()
WHERE org_id = sqlc.arg(org_id) AND owner_user_id = sqlc.arg(user_id)::uuid AND deleted_at IS NULL;

-- name: DeleteUserOwnedSecretVersionsForOrgMember :exec
DELETE FROM secret_versions version
USING secrets secret
WHERE secret.org_id = sqlc.arg(org_id) AND secret.owner_user_id = sqlc.arg(user_id)::uuid
  AND version.org_id = secret.org_id AND version.secret_id = secret.id;

-- name: DeleteUserOwnedSecretChildrenForOrgMember :exec
DELETE FROM secret_grants grant_row
USING secrets secret
WHERE secret.org_id = sqlc.arg(org_id) AND secret.owner_user_id = sqlc.arg(user_id)::uuid
  AND grant_row.org_id = secret.org_id AND grant_row.secret_id = secret.id;

-- name: OrgMemberOwnedSecretsReferenced :one
SELECT EXISTS (
  SELECT 1 FROM secrets secret
  WHERE secret.org_id = sqlc.arg(org_id)
    AND secret.owner_user_id = sqlc.arg(user_id)::uuid
    AND secret.deleted_at IS NULL
    AND (
      EXISTS (SELECT 1 FROM model_provider_configs config WHERE config.org_id = secret.org_id AND config.credential_secret_id = secret.id)
      OR EXISTS (SELECT 1 FROM machine_pools pool WHERE pool.org_id = secret.org_id AND pool.provider_auth_secret_id = secret.id)
      OR EXISTS (SELECT 1 FROM integration_installs install WHERE install.org_id = secret.org_id AND install.credential_secret_id = secret.id)
    )
) AS is_referenced;

-- name: RemoveProjectMembership :one
DELETE FROM project_memberships
WHERE org_id = sqlc.arg(org_id) AND project_id = sqlc.arg(project_id) AND org_membership_id = sqlc.arg(org_membership_id)
RETURNING org_id, project_id, org_membership_id, role, created_at;

-- name: ListProjectMembershipsForUser :many
SELECT pm.project_id, p.name AS project_name, pm.role, pm.created_at
FROM project_memberships pm
JOIN org_memberships om ON om.org_id = pm.org_id AND om.id = pm.org_membership_id
JOIN projects p ON p.org_id = pm.org_id AND p.id = pm.project_id
WHERE pm.org_id = sqlc.arg(org_id) AND om.user_id = sqlc.arg(user_id)::uuid
  AND p.deleted_at IS NULL
ORDER BY p.name, p.id;

-- name: ListProjectMembershipsForOrgAPIKey :many
SELECT pm.project_id, p.name AS project_name, pm.role, pm.created_at
FROM project_memberships pm
JOIN org_memberships om ON om.org_id = pm.org_id AND om.id = pm.org_membership_id
JOIN projects p ON p.org_id = pm.org_id AND p.id = pm.project_id
WHERE pm.org_id = sqlc.arg(org_id) AND om.org_api_key_id = sqlc.arg(org_api_key_id)::uuid
  AND p.deleted_at IS NULL
ORDER BY p.name, p.id;

-- name: AddUserOrgMembershipIfMissing :one
INSERT INTO org_memberships(org_id, user_id, role, created_at)
VALUES (sqlc.arg(org_id), sqlc.arg(user_id)::uuid, sqlc.arg(role), transaction_timestamp())
ON CONFLICT (org_id, user_id) WHERE user_id IS NOT NULL
DO NOTHING
RETURNING id, org_id, user_id, role, created_at;

-- name: GetOrgMembershipForUser :one
SELECT id, org_id, user_id, role, created_at
FROM org_memberships
WHERE org_id = sqlc.arg(org_id) AND user_id = sqlc.arg(user_id)::uuid;

-- name: GetOrgMembershipForOrgAPIKey :one
SELECT id, org_id, org_api_key_id, role, created_at
FROM org_memberships
WHERE org_id = sqlc.arg(org_id) AND org_api_key_id = sqlc.arg(org_api_key_id)::uuid;

-- name: LockUserOrgMembership :one
SELECT id, org_id, user_id, role, created_at
FROM org_memberships
WHERE org_id = sqlc.arg(org_id) AND user_id = sqlc.arg(user_id)::uuid
FOR UPDATE;

-- name: CountOrgOwners :one
SELECT count(*)::bigint
FROM org_memberships om
WHERE om.org_id = sqlc.arg(org_id) AND om.role = 'owner';

-- name: CountOwnedOrgMembershipsForUser :one
SELECT count(*)::bigint
FROM org_memberships om
WHERE om.user_id = sqlc.arg(user_id)::uuid AND om.role = 'owner';

-- name: CountOrgMembershipsForUser :one
SELECT count(*)::bigint
FROM org_memberships om
WHERE om.user_id = sqlc.arg(user_id)::uuid;

-- name: ListOrgMembershipsForUser :many
SELECT o.id, o.name, om.role, o.created_at
FROM org_memberships om
JOIN orgs o ON o.id = om.org_id
WHERE om.user_id = sqlc.arg(user_id)::uuid
  AND o.deleted_at IS NULL
ORDER BY o.name, o.id;

-- name: ListOrgMembers :many
WITH listed AS (
 SELECT u.id AS user_id, u.display_name, om.role, om.created_at,
        CASE sqlc.arg(sort_field)::text
          WHEN 'name' THEN lower(u.display_name)
          WHEN 'created_at' THEN to_char(om.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US')
        END::text AS sort_key,
        false AS sort_is_null
 FROM org_memberships om
 JOIN users u ON u.id = om.user_id
 WHERE om.org_id = sqlc.arg(org_id)
   AND u.deleted_at IS NULL
   AND (sqlc.arg(name_pattern)::text = '' OR u.display_name ILIKE sqlc.arg(name_pattern)::text ESCAPE '\')
)
SELECT user_id, display_name, role, created_at, sort_key, sort_is_null
FROM listed
WHERE sqlc.arg(cursor_set)::boolean = false
   OR (sqlc.arg(sort_desc)::boolean = false AND (sort_key, user_id) > (sqlc.arg(cursor_key)::text, sqlc.arg(cursor_id)::uuid))
   OR (sqlc.arg(sort_desc)::boolean = true AND (sort_key, user_id) < (sqlc.arg(cursor_key)::text, sqlc.arg(cursor_id)::uuid))
ORDER BY CASE WHEN NOT sqlc.arg(sort_desc)::boolean THEN sort_key END ASC,
         CASE WHEN sqlc.arg(sort_desc)::boolean THEN sort_key END DESC,
         CASE WHEN NOT sqlc.arg(sort_desc)::boolean THEN user_id END ASC,
         CASE WHEN sqlc.arg(sort_desc)::boolean THEN user_id END DESC
LIMIT sqlc.arg(row_limit)::bigint;

-- name: CreatePersonalAccessToken :one
INSERT INTO personal_access_tokens(user_id, name, token_id, token_hash, created_at)
VALUES (sqlc.arg(user_id), sqlc.arg(name), sqlc.arg(token_id), sqlc.arg(token_hash), statement_timestamp())
RETURNING id, user_id, name, token_id, token_hash, created_at, last_used_at, revoked_at;

-- name: AuthenticatePersonalAccessToken :one
WITH authenticated AS MATERIALIZED (
  SELECT pat.user_id, pat.id AS personal_access_token_id, pat.last_used_at
  FROM personal_access_tokens pat
  WHERE pat.token_hash = sqlc.arg(token_hash)
    AND pat.revoked_at IS NULL
  LIMIT 1
), touched AS (
  UPDATE personal_access_tokens token
  SET last_used_at = transaction_timestamp()
  FROM authenticated
  WHERE token.id = authenticated.personal_access_token_id
    AND token.revoked_at IS NULL
    AND (
      token.last_used_at IS NULL
      OR token.last_used_at < transaction_timestamp() - (sqlc.arg(touch_interval_seconds)::bigint * interval '1 second')
    )
  RETURNING token.id
)
SELECT user_id, personal_access_token_id, last_used_at
FROM authenticated;

-- name: RevokePersonalAccessTokensForUser :exec
UPDATE personal_access_tokens
SET revoked_at = statement_timestamp()
WHERE user_id = sqlc.arg(user_id)
  AND revoked_at IS NULL;

-- name: ListPersonalAccessTokensForUser :many
SELECT id, user_id, name, token_id, token_hash, created_at, last_used_at, revoked_at
FROM personal_access_tokens
WHERE user_id = sqlc.arg(user_id)
  AND (
    sqlc.narg(cursor_created_at)::timestamptz IS NULL
    OR (created_at, id) < (sqlc.narg(cursor_created_at)::timestamptz, sqlc.narg(cursor_id)::uuid)
  )
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(row_limit)::bigint;

-- name: RevokePersonalAccessToken :one
UPDATE personal_access_tokens
SET revoked_at = COALESCE(revoked_at, transaction_timestamp())
WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id)
RETURNING id, user_id, name, token_id, token_hash, created_at, last_used_at, revoked_at;

-- name: CreateBrowserSession :one
INSERT INTO browser_sessions(user_id, token_hash, csrf_token_hash, created_at, last_seen_at, expires_at)
VALUES (
  sqlc.arg(user_id),
  sqlc.arg(token_hash),
  sqlc.arg(csrf_token_hash),
  statement_timestamp(),
  statement_timestamp(),
  statement_timestamp() + (sqlc.arg(ttl_milliseconds)::bigint * interval '1 millisecond')
)
RETURNING id, user_id, token_hash, csrf_token_hash, created_at, last_seen_at, expires_at, revoked_at;

-- name: AuthenticateBrowserSession :one
WITH authenticated AS MATERIALIZED (
  SELECT bs.user_id, bs.id AS browser_session_id, bs.csrf_token_hash, bs.last_seen_at
  FROM browser_sessions bs
  WHERE bs.token_hash = sqlc.arg(token_hash)
    AND bs.revoked_at IS NULL
    AND bs.expires_at > statement_timestamp()
    AND bs.last_seen_at > statement_timestamp() - (sqlc.arg(idle_timeout_seconds)::bigint * interval '1 second')
  LIMIT 1
), touch_candidate AS MATERIALIZED (
  SELECT session.id
  FROM browser_sessions session
  JOIN authenticated ON authenticated.browser_session_id = session.id
  WHERE session.revoked_at IS NULL
    AND session.expires_at > statement_timestamp()
    AND session.last_seen_at > statement_timestamp() - (sqlc.arg(idle_timeout_seconds)::bigint * interval '1 second')
    AND session.last_seen_at < statement_timestamp() - (sqlc.arg(touch_interval_seconds)::bigint * interval '1 second')
  FOR UPDATE OF session SKIP LOCKED
), touched AS (
  UPDATE browser_sessions session
  SET last_seen_at = statement_timestamp()
  FROM touch_candidate
  WHERE session.id = touch_candidate.id
  RETURNING session.id
)
SELECT authenticated.user_id, authenticated.browser_session_id,
  authenticated.csrf_token_hash, authenticated.last_seen_at
FROM authenticated
LEFT JOIN touched ON true;

-- name: GetActiveBrowserSessionForUserByID :one
SELECT id
FROM browser_sessions
WHERE id = sqlc.arg(id)
  AND user_id = sqlc.arg(user_id)
  AND revoked_at IS NULL
  AND expires_at > statement_timestamp()
  AND last_seen_at > statement_timestamp() - (sqlc.arg(idle_timeout_seconds)::bigint * interval '1 second');

-- name: RevokeBrowserSession :exec
UPDATE browser_sessions
SET revoked_at = statement_timestamp()
WHERE token_hash = sqlc.arg(token_hash) AND revoked_at IS NULL;

-- name: RevokeBrowserSessionsForUser :exec
UPDATE browser_sessions
SET revoked_at = statement_timestamp()
WHERE user_id = sqlc.arg(user_id)
  AND revoked_at IS NULL;

-- name: CreateAuthDeviceFlow :one
INSERT INTO auth_device_flows(device_code_hash, user_code_hash, client_id, client_name, token_name, created_at, expires_at)
VALUES (
  sqlc.arg(device_code_hash),
  sqlc.arg(user_code_hash),
  sqlc.arg(client_id),
  sqlc.arg(client_name),
  sqlc.arg(token_name),
  transaction_timestamp(),
  transaction_timestamp() + (sqlc.arg(ttl_seconds)::bigint * interval '1 second')
)
RETURNING id, device_code_hash, user_code_hash, client_name, token_name, created_at, expires_at, approved_by_user_id, approved_browser_session_id, approved_at, denied_at, consumed_at, last_polled_at, client_id,
  greatest(0, extract(epoch FROM expires_at - transaction_timestamp()))::bigint AS expires_in_seconds;

-- name: GetAuthDeviceFlowByUserCodeForUpdate :one
SELECT id, device_code_hash, user_code_hash, client_name, token_name, created_at, expires_at, approved_by_user_id, approved_browser_session_id, approved_at, denied_at, consumed_at, last_polled_at, client_id
FROM auth_device_flows
WHERE user_code_hash = sqlc.arg(user_code_hash)
  AND approved_at IS NULL
  AND denied_at IS NULL
  AND consumed_at IS NULL
  AND expires_at > transaction_timestamp()
FOR UPDATE;

-- name: GetAuthDeviceFlowByUserCode :one
SELECT id, device_code_hash, user_code_hash, client_name, token_name, created_at, expires_at, approved_by_user_id, approved_browser_session_id, approved_at, denied_at, consumed_at, last_polled_at, client_id
FROM auth_device_flows
WHERE user_code_hash = sqlc.arg(user_code_hash)
  AND approved_at IS NULL
  AND denied_at IS NULL
  AND consumed_at IS NULL
  AND expires_at > transaction_timestamp();

-- name: GetAuthDeviceFlowByDeviceCodeForUpdate :one
SELECT id, device_code_hash, user_code_hash, client_name, token_name, created_at, expires_at, approved_by_user_id, approved_browser_session_id, approved_at, denied_at, consumed_at, last_polled_at, client_id
FROM auth_device_flows
WHERE device_code_hash = sqlc.arg(device_code_hash)
FOR UPDATE;

-- name: GetAuthDeviceFlowPollState :one
SELECT coalesce(consumed_at IS NOT NULL OR expires_at <= statement_timestamp(), false)::boolean AS expired,
       coalesce(
         last_polled_at IS NOT NULL
           AND last_polled_at > statement_timestamp() - sqlc.arg(poll_interval_seconds)::bigint * interval '1 second',
         false
       )::boolean AS polled_too_soon
FROM auth_device_flows
WHERE id = sqlc.arg(id);

-- name: ApproveAuthDeviceFlow :one
UPDATE auth_device_flows
SET approved_by_user_id = sqlc.arg(approved_by_user_id),
    approved_browser_session_id = sqlc.arg(approved_browser_session_id),
    approved_at = statement_timestamp()
WHERE id = sqlc.arg(id)
  AND approved_at IS NULL
  AND denied_at IS NULL
  AND consumed_at IS NULL
  AND expires_at > statement_timestamp()
RETURNING id, device_code_hash, user_code_hash, client_name, token_name, created_at, expires_at, approved_by_user_id, approved_browser_session_id, approved_at, denied_at, consumed_at, last_polled_at, client_id;

-- name: DenyAuthDeviceFlow :one
UPDATE auth_device_flows
SET denied_at = statement_timestamp()
WHERE id = sqlc.arg(id)
  AND approved_at IS NULL
  AND denied_at IS NULL
  AND consumed_at IS NULL
  AND expires_at > statement_timestamp()
RETURNING id, device_code_hash, user_code_hash, client_name, token_name, created_at, expires_at, approved_by_user_id, approved_browser_session_id, approved_at, denied_at, consumed_at, last_polled_at, client_id;

-- name: DenyInvalidatedApprovedAuthDeviceFlow :execrows
UPDATE auth_device_flows
SET denied_at = statement_timestamp()
WHERE id = sqlc.arg(id)
  AND approved_at IS NOT NULL
  AND denied_at IS NULL
  AND consumed_at IS NULL
  AND expires_at > statement_timestamp();

-- name: MarkAuthDeviceFlowPolled :execrows
UPDATE auth_device_flows
SET last_polled_at = statement_timestamp()
WHERE id = sqlc.arg(id)
  AND consumed_at IS NULL
  AND expires_at > statement_timestamp()
  AND (
    last_polled_at IS NULL
    OR last_polled_at <= statement_timestamp() - (sqlc.arg(poll_interval_seconds)::bigint * interval '1 second')
  );

-- name: ConsumeAuthDeviceFlow :execrows
UPDATE auth_device_flows
SET consumed_at = statement_timestamp()
WHERE id = sqlc.arg(id)
  AND consumed_at IS NULL
  AND expires_at > statement_timestamp();

-- name: DeleteExpiredAuthDeviceFlows :execrows
WITH candidates AS (
    SELECT id
    FROM auth_device_flows
    WHERE expires_at <= transaction_timestamp()
    ORDER BY expires_at, id
    LIMIT sqlc.arg(limit_count)
)
DELETE FROM auth_device_flows flow
USING candidates
WHERE flow.id = candidates.id;

-- name: CreateOrgInvitation :one
INSERT INTO org_invitations(org_id, email, normalized_email, org_role, created_at)
VALUES (sqlc.arg(org_id), sqlc.arg(email), sqlc.arg(normalized_email), sqlc.arg(org_role), statement_timestamp())
RETURNING id, org_id, email, normalized_email, org_role, created_at;

-- name: GetPendingOrgInvitationByEmail :one
SELECT id, org_id, email, normalized_email, org_role, created_at
FROM org_invitations
WHERE org_id = sqlc.arg(org_id)
  AND normalized_email = sqlc.arg(normalized_email);

-- name: ListPendingOrgInvitationsForEmails :many
SELECT invitation.id,
       invitation.org_id,
       org.name AS org_name,
       invitation.email,
       invitation.normalized_email,
       invitation.org_role,
       invitation.created_at
FROM org_invitations invitation
JOIN orgs org ON org.id = invitation.org_id AND org.deleted_at IS NULL
WHERE invitation.normalized_email = ANY(sqlc.arg(normalized_emails)::text[])
  AND (
    sqlc.narg(cursor_created_at)::timestamptz IS NULL
    OR (invitation.created_at, invitation.id) > (sqlc.narg(cursor_created_at)::timestamptz, sqlc.narg(cursor_id)::uuid)
  )
ORDER BY invitation.created_at ASC, invitation.id ASC
LIMIT sqlc.arg(row_limit)::bigint;

-- name: ListOrgInvitations :many
SELECT id, org_id, email, normalized_email, org_role, created_at
FROM org_invitations
WHERE org_id = sqlc.arg(org_id)
  AND (
    sqlc.narg(cursor_created_at)::timestamptz IS NULL
    OR (created_at, id) < (sqlc.narg(cursor_created_at)::timestamptz, sqlc.narg(cursor_id)::uuid)
  )
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(row_limit)::bigint;

-- name: ConsumeOrgInvitationForEmail :one
WITH consumed AS (
  DELETE FROM org_invitations
  WHERE org_invitations.id = sqlc.arg(id)
    AND org_invitations.normalized_email = sqlc.arg(normalized_email)
  RETURNING id, org_id, email, normalized_email, org_role, created_at
)
SELECT consumed.id,
       consumed.org_id,
       org.name AS org_name,
       consumed.email,
       consumed.normalized_email,
       consumed.org_role,
       consumed.created_at
FROM consumed
JOIN orgs org ON org.id = consumed.org_id AND org.deleted_at IS NULL;

-- name: DeleteOrgInvitation :one
DELETE FROM org_invitations
WHERE id = sqlc.arg(id)
  AND org_id = sqlc.arg(org_id)
RETURNING id, org_id, email, normalized_email, org_role, created_at;

-- name: LockOrg :one
SELECT id
FROM orgs
WHERE id = $1 AND deleted_at IS NULL
FOR UPDATE;

-- name: CountOrgMemberships :one
SELECT count(*)::bigint
FROM org_memberships
WHERE org_id = $1;

-- name: ListProjectAuthorizationRolesForPrincipal :many
SELECT role
FROM principal_project_authorization_roles
WHERE org_id = sqlc.arg(org_id)
  AND project_id = sqlc.arg(project_id)
  AND (
    (sqlc.narg(user_id)::uuid IS NOT NULL AND user_id = sqlc.narg(user_id)::uuid)
    OR (sqlc.narg(org_api_key_id)::uuid IS NOT NULL AND org_api_key_id = sqlc.narg(org_api_key_id)::uuid)
  );

-- name: GetOrgAuthorizationRoleForPrincipal :one
SELECT role
FROM org_memberships
WHERE org_id = sqlc.arg(org_id)
  AND (
    (sqlc.narg(user_id)::uuid IS NOT NULL AND user_id = sqlc.narg(user_id)::uuid)
    OR (sqlc.narg(org_api_key_id)::uuid IS NOT NULL AND org_api_key_id = sqlc.narg(org_api_key_id)::uuid)
  );

-- name: ListAgentInputProducerAuthorizationRoles :many
SELECT roles.role
FROM principal_project_authorization_roles roles
WHERE roles.project_id = sqlc.arg(project_id)
  AND (
    (sqlc.narg(user_id)::uuid IS NOT NULL AND roles.user_id = sqlc.narg(user_id)::uuid)
    OR (sqlc.narg(org_api_key_id)::uuid IS NOT NULL AND roles.org_api_key_id = sqlc.narg(org_api_key_id)::uuid)
  );

-- name: MachineProjectVisibleToPrincipal :one
SELECT EXISTS (
  SELECT 1
  FROM project_machine_grants pmg
  JOIN machines machine
    ON machine.org_id = pmg.org_id
   AND machine.id = pmg.machine_id
   AND machine.deleted_at IS NULL
   AND machine.lifecycle_state = 'active'
  JOIN principal_project_authorization_roles roles
    ON roles.org_id = pmg.org_id
   AND roles.project_id = pmg.project_id
   AND (
     (sqlc.narg(user_id)::uuid IS NOT NULL AND roles.user_id = sqlc.narg(user_id)::uuid)
     OR (sqlc.narg(org_api_key_id)::uuid IS NOT NULL AND roles.org_api_key_id = sqlc.narg(org_api_key_id)::uuid)
   )
  WHERE pmg.org_id = sqlc.arg(org_id)
    AND pmg.machine_id = sqlc.arg(machine_id)
    AND roles.role IN ('admin', 'developer', 'operator', 'viewer')
);

-- name: GetOrgAuthorizationRole :one
SELECT role
FROM org_memberships om
WHERE om.org_id = sqlc.arg(org_id) AND om.user_id = sqlc.arg(user_id)::uuid;
