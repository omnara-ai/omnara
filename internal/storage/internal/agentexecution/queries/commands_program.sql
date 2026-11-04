-- name: LockProgramProcess :one
SELECT p.id,p.tool_call_id,(p.execution_granted_at IS NOT NULL AND p.state IN ('starting','running'))::boolean AS live
FROM processes p WHERE p.agent_id=sqlc.arg(agent_id) AND p.id=sqlc.arg(id) FOR SHARE;

-- name: InsertProgramTool :one
INSERT INTO tool_calls(id,agent_id,model_output_id,provider_call_id,name,input,type,state,caller,
 parent_tool_call_id,program_key,created_at)
SELECT sqlc.arg(id),c.agent_id,c.model_output_id,'program:'||c.id::text||':'||sqlc.arg(key)::text,sqlc.arg(name),
 sqlc.arg(input)::jsonb,sqlc.arg(type),'awaiting_authorization','program',c.id,sqlc.arg(key),statement_timestamp()
FROM tool_calls c WHERE c.agent_id=sqlc.arg(agent_id) AND c.id=sqlc.arg(parent_id) AND c.caller='model'
ON CONFLICT ON CONSTRAINT tool_calls_program_key_key DO NOTHING
RETURNING id;

-- name: ReadProgramTool :one
SELECT c.id,c.model_output_id,c.name,c.input,c.type,c.state,c.parent_tool_call_id,coalesce(c.program_key,'')::text AS program_key,
 c.program_claim_id,c.program_claim_expires_at,coalesce(c.program_claim_expires_at<=statement_timestamp(),false)::boolean AS claim_expired,
 c.created_at,p.id AS process_id,r.id AS result_id,r.outcome,r.completed_at
FROM tool_calls c JOIN processes p ON p.agent_id=c.agent_id AND p.tool_call_id=c.parent_tool_call_id
LEFT JOIN tool_call_results r ON r.agent_id=c.agent_id AND r.tool_call_id=c.id
WHERE c.agent_id=sqlc.arg(agent_id) AND c.caller='program' AND (c.id=sqlc.narg(id)
 OR (c.parent_tool_call_id=sqlc.narg(parent_id) AND c.program_key=sqlc.narg(key)))
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

-- name: ListProgramToolCandidates :many
SELECT c.id,c.agent_id,a.project_id,p.id AS process_id FROM tool_calls c
JOIN agents a ON a.id=c.agent_id AND a.state='active'
JOIN processes p ON p.agent_id=c.agent_id AND p.tool_call_id=c.parent_tool_call_id
 AND p.execution_granted_at IS NOT NULL AND p.state IN ('starting','running')
WHERE c.caller='program' AND c.state<>'completed' AND (c.state='ready' OR (c.state='awaiting_authorization'
 AND (c.program_claim_id IS NULL OR c.program_claim_expires_at<=statement_timestamp())))
ORDER BY c.created_at,c.id LIMIT sqlc.arg(limit_count);

-- name: ListProgramToolReapable :many
SELECT c.id,c.agent_id,a.project_id FROM tool_calls c JOIN agents a ON a.id=c.agent_id
JOIN processes p ON p.agent_id=c.agent_id AND p.tool_call_id=c.parent_tool_call_id
WHERE c.caller='program' AND c.state<>'completed' AND (a.state<>'active' OR p.execution_granted_at IS NULL
 OR p.state NOT IN ('starting','running') OR (c.state='waiting' AND c.program_claim_expires_at<=statement_timestamp()))
ORDER BY c.created_at,c.id LIMIT sqlc.arg(limit_count);

