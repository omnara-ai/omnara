-- name: ReadExecutionInput :one
SELECT state,input_kind,delivery_mode,actor_id
FROM agent_inputs WHERE agent_id=sqlc.arg(agent_id) AND
 id=sqlc.arg(id);

-- name: FindExecutionInput :one
SELECT id
FROM agent_inputs WHERE agent_id=sqlc.arg(agent_id) AND
 idempotency_scope=sqlc.narg(scope) AND input_idempotency_key=sqlc.narg(key);

-- name: ReceiveExecutionInput :one
INSERT INTO agent_inputs(id,project_id,agent_id,state,input_rank,actor_id,input_kind,delivery_mode,
 integration_target_id,agent_config_id,idempotency_scope,input_idempotency_key,queued_at,metadata)
SELECT sqlc.arg(id),a.project_id,a.id,'received',CASE WHEN sqlc.arg(kind)::text='content' THEN coalesce((
 SELECT i.input_rank+1024 FROM agent_inputs i WHERE i.agent_id=a.id AND i.state='received'
 AND i.input_kind='content' AND i.delivery_mode=sqlc.arg(mode)
 ORDER BY i.input_rank DESC,i.queued_at DESC,i.id DESC LIMIT 1),1024) ELSE 1024 END,
 sqlc.narg(actor_id),sqlc.arg(kind),sqlc.arg(mode),sqlc.narg(target_id),sqlc.narg(config_id),
 sqlc.narg(scope),sqlc.narg(key),statement_timestamp(),sqlc.arg(metadata)::jsonb
FROM agents a WHERE a.id=sqlc.arg(agent_id) AND a.project_id=sqlc.arg(project_id) AND a.state='active'
RETURNING id,queued_at;

-- name: SelectExecutionAdmission :many
WITH selected AS MATERIALIZED (
 SELECT id,actor_id,integration_target_id,input_rank,queued_at FROM agent_inputs
 WHERE agent_id=sqlc.arg(agent_id) AND input_kind='content' AND state='received'
 AND delivery_mode=sqlc.arg(mode)
 ORDER BY input_rank,queued_at,id LIMIT sqlc.narg(row_limit)::bigint
)
SELECT s.id,s.actor_id,s.integration_target_id,
 sqlc.arg(mode)::text='queued' AND (SELECT i.id FROM agent_inputs i
 WHERE i.agent_id=sqlc.arg(agent_id) AND i.input_kind='content' AND i.state='received'
 AND i.delivery_mode='steering' ORDER BY i.input_rank,i.queued_at,i.id LIMIT 1) IS NOT NULL AS remaining_steering,
 EXISTS(SELECT 1 FROM (SELECT i.id FROM agent_inputs i WHERE i.agent_id=sqlc.arg(agent_id)
 AND i.input_kind='content' AND i.state='received' AND i.delivery_mode='queued'
 ORDER BY i.input_rank DESC,i.queued_at DESC,i.id DESC LIMIT 1) pending
 WHERE pending.id IS DISTINCT FROM (SELECT id FROM selected WHERE sqlc.arg(mode)::text='queued' LIMIT 1)) AS remaining_queued
FROM selected s ORDER BY s.input_rank,s.queued_at,s.id;

-- name: GetExecutionBacklogInputs :many
SELECT id,state,input_kind,delivery_mode,input_rank,queued_at,admitted_event_id FROM agent_inputs
WHERE agent_id=sqlc.arg(agent_id) AND id=ANY(sqlc.arg(ids)::uuid[]);

-- name: FirstExecutionBacklogRank :one
SELECT input_rank FROM agent_inputs
WHERE agent_id=sqlc.arg(agent_id) AND input_kind='content' AND state='received'
 AND delivery_mode=sqlc.arg(mode) AND id<>sqlc.arg(skip_id)
ORDER BY input_rank,queued_at,id LIMIT 1;

-- name: LastExecutionBacklogRank :one
SELECT input_rank FROM agent_inputs
WHERE agent_id=sqlc.arg(agent_id) AND input_kind='content' AND state='received'
 AND delivery_mode=sqlc.arg(mode) AND id<>sqlc.arg(skip_id)
ORDER BY input_rank DESC,queued_at DESC,id DESC LIMIT 1;

-- name: PreviousExecutionBacklogRank :one
SELECT input_rank FROM agent_inputs
WHERE agent_id=sqlc.arg(agent_id) AND input_kind='content' AND state='received'
 AND delivery_mode=sqlc.arg(mode) AND id<>sqlc.arg(skip_id)
 AND (input_rank,queued_at,id)<(sqlc.arg(rank)::bigint,sqlc.arg(queued_at)::timestamptz,sqlc.arg(anchor_id)::uuid)
ORDER BY input_rank DESC,queued_at DESC,id DESC LIMIT 1;

-- name: NextExecutionBacklogRank :one
SELECT input_rank FROM agent_inputs
WHERE agent_id=sqlc.arg(agent_id) AND input_kind='content' AND state='received'
 AND delivery_mode=sqlc.arg(mode) AND id<>sqlc.arg(skip_id)
 AND (input_rank,queued_at,id)>(sqlc.arg(rank)::bigint,sqlc.arg(queued_at)::timestamptz,sqlc.arg(anchor_id)::uuid)
ORDER BY input_rank,queued_at,id LIMIT 1;

-- name: RebalanceExecutionBacklog :execrows
WITH ranked AS (
 SELECT pending.id,row_number() OVER (ORDER BY pending.input_rank,pending.queued_at,pending.id)*1024 AS rank
 FROM agent_inputs pending WHERE pending.agent_id=sqlc.arg(agent_id)
 AND pending.input_kind='content' AND pending.state='received' AND pending.delivery_mode=sqlc.arg(mode)
)
UPDATE agent_inputs i SET input_rank=r.rank FROM ranked r
WHERE i.agent_id=sqlc.arg(agent_id) AND i.id=r.id
 AND i.input_kind='content' AND i.state='received' AND i.delivery_mode=sqlc.arg(mode);

-- name: ChangeExecutionInput :execrows
UPDATE agent_inputs SET delivery_mode=sqlc.arg(mode),input_rank=sqlc.arg(rank),
 state=CASE WHEN sqlc.arg(cancel)::boolean THEN 'canceled' ELSE state END,
 canceled_at=CASE WHEN sqlc.arg(cancel)::boolean THEN statement_timestamp() ELSE NULL END
WHERE agent_id=sqlc.arg(agent_id) AND id=sqlc.arg(id) AND state='received' AND input_kind='content'
 AND (delivery_mode,input_rank,state) IS DISTINCT FROM
 (sqlc.arg(mode),sqlc.arg(rank),CASE WHEN sqlc.arg(cancel)::boolean THEN 'canceled' ELSE state END);

-- name: ResolveExecutionInput :execrows
UPDATE agent_inputs i SET state='resolved',admitted_event_id=sqlc.arg(event_id),
 admitted_at=e.created_at,resolved_at=e.created_at,
 opening_input_ids=sqlc.narg(opening_ids)::uuid[],opening_event_sequence=sqlc.narg(opening_sequence)::bigint
FROM agent_events e WHERE i.agent_id=sqlc.arg(agent_id) AND i.id=sqlc.arg(id) AND i.state='received'
 AND e.agent_id=i.agent_id AND e.id=sqlc.arg(event_id) AND e.agent_input_id=i.id;

-- name: ActivateExecutionConfig :execrows
UPDATE agents a SET current_config_id=c.id,updated_at=statement_timestamp()
FROM agent_configs c WHERE a.id=sqlc.arg(agent_id) AND a.project_id=sqlc.arg(project_id)
 AND c.project_id=a.project_id AND c.id=sqlc.arg(config_id);

-- name: CountPendingExecutionContent :one
SELECT count(*) FROM (
    SELECT 1 FROM agent_inputs
    WHERE agent_id=sqlc.arg(agent_id) AND state='received' AND input_kind='content'
    ORDER BY delivery_mode,input_rank,queued_at,id LIMIT sqlc.arg(capacity)::bigint
) pending;
