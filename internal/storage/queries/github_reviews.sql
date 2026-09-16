-- The operation owner holds the live installation and agent locks before admission.
-- Null identities are retained as historical facts; only a running creator blocks
-- another omitted-ID action. Native pending state is checked by the gateway.
-- Historical scope remains readable after cancellation/disconnect so a provider
-- response can record facts. This query confers no permission for further sends.
-- name: GetGitHubReviewOperationScope :one
-- @sqlc-vet-disable integration-installs-deleted-at integration-apps-deleted-at integration-targets-deleted-at
-- Historical correlation must survive retirement; separate live checks authorize further dispatch.
SELECT install.project_id, app.connector_key, app.provider, call.name AS tool_name, call.input,
  target.parent_channel_id, definition.implementation_key,
  coalesce(parent_definition.implementation_key, '') AS parent_implementation_key
FROM tool_calls call
JOIN agents agent ON agent.id = call.agent_id
JOIN integration_installs install ON install.project_id = agent.project_id
  AND install.id = sqlc.arg(integration_install_id) AND install.integration_kind = 'managed'
JOIN integration_apps app ON app.org_id = install.org_id AND app.id = install.integration_app_id
  AND app.id = sqlc.arg(integration_app_id)
JOIN integration_targets target ON target.project_id = install.project_id
  AND target.integration_install_id = install.id AND target.id = sqlc.arg(channel_id)
JOIN integration_channel_definitions definition ON definition.project_id = target.project_id
  AND definition.integration_install_id = install.id AND definition.id = target.channel_definition_id
LEFT JOIN integration_targets parent ON parent.project_id = target.project_id
  AND parent.integration_install_id = install.id AND parent.id = target.parent_channel_id
LEFT JOIN integration_channel_definitions parent_definition ON parent_definition.project_id = parent.project_id
  AND parent_definition.integration_install_id = install.id AND parent_definition.id = parent.channel_definition_id
WHERE call.agent_id = sqlc.arg(agent_id) AND call.id = sqlc.arg(request_id)
  AND call.type = 'built_in'
  AND ((call.name = 'send_channel_message' AND call.state IN ('running', 'completed'))
    OR (call.name = 'read_channel' AND call.state = 'running'));

-- name: HasRunningGitHubReviewCreation :one
SELECT EXISTS (
  SELECT 1
  FROM github_pr_reviews review
  JOIN tool_calls call ON call.agent_id = review.agent_id AND call.id = review.creating_tool_call_id
  WHERE review.project_id = sqlc.arg(project_id)
    AND review.agent_id = sqlc.arg(agent_id)
    AND review.pr_channel_id = sqlc.arg(pr_channel_id)
    AND review.creating_tool_call_id <> sqlc.arg(issuing_tool_call_id)
    AND call.state = 'running'
);

-- name: InsertGitHubReviewCreator :one
INSERT INTO github_pr_reviews (
  project_id, agent_id, creating_tool_call_id, integration_install_id,
  pr_channel_id, creating_binding_id, commit_id
) VALUES (
  sqlc.arg(project_id), sqlc.arg(agent_id), sqlc.arg(creating_tool_call_id),
  sqlc.arg(integration_install_id), sqlc.arg(pr_channel_id), sqlc.arg(creating_binding_id), sqlc.arg(commit_id)
)
ON CONFLICT (agent_id, creating_tool_call_id) DO NOTHING
RETURNING project_id, agent_id, creating_tool_call_id, integration_install_id,
  pr_channel_id, creating_binding_id, commit_id, provider_review_id, created_at;

-- name: GetGitHubReviewCreator :one
SELECT project_id, agent_id, creating_tool_call_id, integration_install_id,
  pr_channel_id, creating_binding_id, commit_id, provider_review_id, created_at
FROM github_pr_reviews
WHERE project_id = sqlc.arg(project_id)
  AND agent_id = sqlc.arg(agent_id)
  AND creating_tool_call_id = sqlc.arg(creating_tool_call_id);

-- name: GetGitHubReviewByProviderID :one
SELECT project_id, agent_id, creating_tool_call_id, integration_install_id,
  pr_channel_id, creating_binding_id, commit_id, provider_review_id, created_at
FROM github_pr_reviews
WHERE project_id = sqlc.arg(project_id)
  AND integration_install_id = sqlc.arg(integration_install_id)
  AND provider_review_id = sqlc.arg(provider_review_id);

-- Bounded native IDs/markers supplied by one verified provider observation.
-- Query all matches together; no recent-history window can forget an old draft.
-- name: ListGitHubReviewObservations :many
SELECT project_id, agent_id, creating_tool_call_id, integration_install_id,
  pr_channel_id, creating_binding_id, commit_id, provider_review_id, created_at
FROM github_pr_reviews
WHERE project_id = sqlc.arg(project_id)
  AND integration_install_id = sqlc.arg(integration_install_id)
  AND pr_channel_id = sqlc.arg(pr_channel_id)
  AND (provider_review_id = ANY(sqlc.arg(provider_review_ids)::text[])
    OR (agent_id = sqlc.arg(agent_id)
      AND creating_tool_call_id = ANY(sqlc.arg(creating_tool_call_ids)::uuid[])));

-- A fact may arrive after cancellation. This write records identity only and is
-- idempotent for an identical observation; it never changes the original creator.
-- name: RecordGitHubReviewIdentity :one
UPDATE github_pr_reviews
SET provider_review_id = sqlc.arg(provider_review_id)
WHERE project_id = sqlc.arg(project_id)
  AND agent_id = sqlc.arg(agent_id)
  AND creating_tool_call_id = sqlc.arg(creating_tool_call_id)
  AND (provider_review_id IS NULL OR provider_review_id = sqlc.arg(provider_review_id))
RETURNING project_id, agent_id, creating_tool_call_id, integration_install_id,
  pr_channel_id, creating_binding_id, commit_id, provider_review_id, created_at;
