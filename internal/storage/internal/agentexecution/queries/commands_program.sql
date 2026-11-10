-- name: LockProgramProcess :one
SELECT p.id,p.tool_call_id,(p.execution_granted_at IS NOT NULL AND p.state IN ('starting','running'))::boolean AS live
FROM processes p WHERE p.agent_id=sqlc.arg(agent_id) AND p.id=sqlc.arg(id) FOR SHARE;

-- name: InsertProgramTool :one
INSERT INTO tool_calls(id,project_id,agent_id,provider_call_id,name,input,type,state,caller,parent_tool_call_id,created_at)
SELECT sqlc.arg(id),c.project_id,c.agent_id,sqlc.arg(key),sqlc.arg(name),sqlc.arg(input)::jsonb,sqlc.arg(type),
 'awaiting_authorization','program',c.id,statement_timestamp()
FROM tool_calls c WHERE c.agent_id=sqlc.arg(agent_id) AND c.id=sqlc.arg(parent_id) AND c.caller='model'
ON CONFLICT (agent_id,parent_tool_call_id,provider_call_id) WHERE caller='program' DO NOTHING
RETURNING id;

-- name: CountOpenProgramTools :one
SELECT count(*) FROM tool_calls WHERE agent_id=sqlc.arg(agent_id) AND caller='program'
 AND parent_tool_call_id=sqlc.arg(parent_id)::uuid AND state<>'completed';

-- name: ReadProgramTool :one
SELECT c.id,c.name,c.input,c.type,c.state,c.parent_tool_call_id,c.provider_call_id,
 c.program_claim_id,c.program_claim_expires_at,coalesce(c.program_claim_expires_at<=statement_timestamp(),false)::boolean AS claim_expired,
 c.created_at,p.id AS process_id,r.id AS result_id,r.outcome,r.completed_at
FROM tool_calls c JOIN processes p ON p.agent_id=c.agent_id AND p.tool_call_id=c.parent_tool_call_id
LEFT JOIN tool_call_results r ON r.agent_id=c.agent_id AND r.tool_call_id=c.id
WHERE c.agent_id=sqlc.arg(agent_id) AND c.caller='program' AND (c.id=sqlc.narg(id)
 OR (c.parent_tool_call_id=sqlc.narg(parent_id) AND c.provider_call_id=sqlc.narg(key)))
FOR UPDATE OF c;

-- name: TransitionProgramTool :execrows
UPDATE tool_calls c SET state=sqlc.arg(next_state),program_claim_id=sqlc.narg(next_claim_id),
 program_claim_expires_at=CASE WHEN sqlc.narg(next_claim_id)::uuid IS NULL THEN NULL
 ELSE statement_timestamp()+sqlc.arg(lease_microseconds)::bigint*interval '1 microsecond' END
WHERE c.agent_id=sqlc.arg(agent_id) AND c.id=sqlc.arg(id) AND c.caller='program' AND c.state=sqlc.arg(previous_state)
 AND c.program_claim_id IS NOT DISTINCT FROM sqlc.narg(previous_claim_id)::uuid;

-- name: RenewProgramToolClaim :one
UPDATE tool_calls c SET program_claim_expires_at=greatest(c.program_claim_expires_at,
 statement_timestamp()+sqlc.arg(lease_microseconds)::bigint*interval '1 microsecond')
WHERE c.agent_id=sqlc.arg(agent_id) AND c.id=sqlc.arg(id) AND c.caller='program'
 AND c.state IN ('awaiting_authorization','waiting') AND c.program_claim_id=sqlc.arg(claim_id)
 AND c.program_claim_expires_at>statement_timestamp()
 AND EXISTS(SELECT 1 FROM processes p WHERE p.agent_id=c.agent_id AND p.id=sqlc.arg(process_id)
 AND p.tool_call_id=c.parent_tool_call_id AND p.execution_granted_at IS NOT NULL AND p.state IN ('starting','running'))
RETURNING c.program_claim_expires_at;

-- name: ListProgramToolReapable :many
SELECT c.id,c.agent_id,a.project_id,c.created_at FROM tool_calls c JOIN agents a ON a.id=c.agent_id
JOIN processes p ON p.agent_id=c.agent_id AND p.tool_call_id=c.parent_tool_call_id
WHERE c.caller='program' AND c.state<>'completed' AND (a.state<>'active' OR p.execution_granted_at IS NULL
 OR p.state NOT IN ('starting','running')
 OR (c.state='waiting' AND c.type<>'built_in' AND c.program_claim_expires_at<=statement_timestamp()))
 AND (c.created_at,c.id)>(sqlc.arg(after_created_at)::timestamptz,sqlc.arg(after_id)::uuid)
ORDER BY c.created_at,c.id LIMIT sqlc.arg(limit_count);

