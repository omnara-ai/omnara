-- name: CountAgentsCreated :one
SELECT count(*)::bigint AS agent_count
FROM agents agent
WHERE agent.project_id = ANY(sqlc.arg(project_ids)::uuid[])
  AND agent.parent_agent_id IS NULL
  AND agent.created_at >= sqlc.arg(since)::timestamptz
  AND (sqlc.narg(until)::timestamptz IS NULL OR agent.created_at < sqlc.narg(until)::timestamptz);

-- name: CountContentInputs :one
SELECT count(*)::bigint AS input_count
FROM agent_inputs input
JOIN agents agent ON agent.project_id = input.project_id
  AND agent.id = input.agent_id
WHERE input.project_id = ANY(sqlc.arg(project_ids)::uuid[])
  AND input.input_kind = 'content'
  AND agent.parent_agent_id IS NULL
  AND input.queued_at >= sqlc.arg(since)::timestamptz
  AND (sqlc.narg(until)::timestamptz IS NULL OR input.queued_at < sqlc.narg(until)::timestamptz);

-- name: SumTokensUsed :one
SELECT (coalesce(sum(context.input_tokens_total), 0) + coalesce(sum(context.output_tokens_total), 0))::bigint
         AS tokens_used
FROM model_call_contexts context
WHERE context.org_id = sqlc.arg(org_id)
  AND context.project_id = ANY(sqlc.arg(project_ids)::uuid[])
  AND context.created_at >= sqlc.arg(since)::timestamptz
  AND (sqlc.narg(until)::timestamptz IS NULL OR context.created_at < sqlc.narg(until)::timestamptz)
  AND (
    context.input_tokens_total IS NOT NULL
    OR context.output_tokens_total IS NOT NULL
    OR context.provider_reported_cost_usd IS NOT NULL
  );
