-- name: FindExecutionInput :one
SELECT id,state,input_kind,delivery_mode,actor_id,integration_target_id,agent_config_id,
 metadata,queued_at,admitted_event_id,input_rank
FROM agent_inputs WHERE agent_id=sqlc.arg(agent_id) AND
 (id=sqlc.arg(id) OR (idempotency_scope=sqlc.narg(scope) AND input_idempotency_key=sqlc.narg(key)));

-- name: ReceiveExecutionInput :one
INSERT INTO agent_inputs(id,project_id,agent_id,state,input_rank,actor_id,input_kind,delivery_mode,
 integration_target_id,agent_config_id,idempotency_scope,input_idempotency_key,queued_at,metadata)
SELECT sqlc.arg(id),a.project_id,a.id,'received',coalesce((SELECT max(i.input_rank)+1024
 FROM agent_inputs i WHERE i.agent_id=a.id AND i.state='received' AND i.delivery_mode=sqlc.arg(mode)),1024),
 sqlc.narg(actor_id),sqlc.arg(kind),sqlc.arg(mode),sqlc.narg(target_id),sqlc.narg(config_id),
 sqlc.narg(scope),sqlc.narg(key),statement_timestamp(),sqlc.arg(metadata)::jsonb
FROM agents a WHERE a.id=sqlc.arg(agent_id) AND a.project_id=sqlc.arg(project_id) AND a.state='active'
RETURNING id,queued_at;

-- name: ListExecutionBacklog :many
SELECT id,delivery_mode,input_rank,queued_at,integration_target_id,actor_id FROM agent_inputs
WHERE agent_id=sqlc.arg(agent_id) AND input_kind='content' AND state='received'
ORDER BY input_rank,queued_at,id;

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
