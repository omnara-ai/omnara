-- name: ConsumeAgentPanelBootstrapEligibility :execrows
UPDATE users SET agent_panel_bootstrap_eligible = false
WHERE id = sqlc.arg(user_id) AND deleted_at IS NULL AND agent_panel_bootstrap_eligible;

-- name: EnqueueAgentPanelBootstrap :exec
INSERT INTO agent_panel_preferences(user_id, pending_project_id)
VALUES (sqlc.arg(user_id), sqlc.arg(project_id))
ON CONFLICT (user_id) DO NOTHING;

-- name: GetAgentPanelPreference :one
SELECT user_id, agent_id, enabled, revision, pending_project_id
FROM agent_panel_preferences WHERE user_id = sqlc.arg(user_id);

-- name: SetAgentPanelPreference :exec
INSERT INTO agent_panel_preferences(user_id, agent_id, enabled, revision)
VALUES (sqlc.arg(user_id), sqlc.narg(agent_id), sqlc.arg(enabled), 1)
ON CONFLICT (user_id) DO UPDATE
SET agent_id = EXCLUDED.agent_id, enabled = EXCLUDED.enabled,
    revision = agent_panel_preferences.revision + 1, pending_project_id = NULL;

-- name: GetAgentPanelProject :one
SELECT project.id, project.org_id, project.name, org.name AS org_name
FROM projects project JOIN orgs org ON org.id = project.org_id AND org.deleted_at IS NULL
WHERE project.id = sqlc.arg(project_id) AND project.deleted_at IS NULL;

-- name: GetAssistantAgentCreation :one
SELECT user_id, request_id, org_id, project_id, agent_id,
    personal_access_token_id, secret_id, requested_model_id, automatic
FROM assistant_agent_creations
WHERE user_id = sqlc.arg(user_id) AND request_id = sqlc.arg(request_id);

-- name: RecordAssistantAgentCreation :exec
INSERT INTO assistant_agent_creations(
    user_id, request_id, org_id, project_id, agent_id, personal_access_token_id,
    secret_id, requested_model_id, automatic
) VALUES (
    sqlc.arg(user_id), sqlc.arg(request_id), sqlc.arg(org_id), sqlc.arg(project_id),
    sqlc.arg(agent_id), sqlc.arg(personal_access_token_id), sqlc.arg(secret_id),
    sqlc.narg(requested_model_id), sqlc.arg(automatic)
);

-- name: RevokeAssistantTokensForHome :exec
UPDATE personal_access_tokens token SET revoked_at = transaction_timestamp()
FROM assistant_agent_creations creation
WHERE token.id = creation.personal_access_token_id AND token.revoked_at IS NULL
    AND creation.org_id = sqlc.arg(org_id)
    AND (sqlc.narg(project_id)::uuid IS NULL OR creation.project_id = sqlc.narg(project_id));

-- name: DeleteUserAgentPanelPreferences :exec
DELETE FROM agent_panel_preferences WHERE user_id = sqlc.arg(user_id);
