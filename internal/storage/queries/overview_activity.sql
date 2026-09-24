-- name: CountOrgActivity :one
SELECT (
         SELECT count(*)
         FROM agents agent
         WHERE agent.project_id = ANY(sqlc.arg(project_ids)::uuid[])
           AND agent.parent_agent_id IS NULL
           AND agent.created_at >= sqlc.arg(since)::timestamptz
           AND (sqlc.narg(until)::timestamptz IS NULL OR agent.created_at < sqlc.narg(until)::timestamptz)
       )::bigint AS agents_created,
       (
         SELECT count(*)
         FROM agent_inputs input
         JOIN agents agent ON agent.project_id = input.project_id
           AND agent.id = input.agent_id
         WHERE input.project_id = ANY(sqlc.arg(project_ids)::uuid[])
           AND input.input_kind = 'content'
           AND input.idempotency_scope IS DISTINCT FROM 'subagent_message'
           AND agent.parent_agent_id IS NULL
           AND input.queued_at >= sqlc.arg(since)::timestamptz
           AND (sqlc.narg(until)::timestamptz IS NULL OR input.queued_at < sqlc.narg(until)::timestamptz)
       )::bigint AS messages_sent;
