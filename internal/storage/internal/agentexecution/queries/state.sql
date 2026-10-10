-- name: LoadExecutionScope :one
SELECT a.root_agent_id
FROM agents a WHERE a.project_id=$1 AND a.id=$2;

-- name: LoadExecutionBoundaries :one
SELECT coalesce((SELECT max(e.sequence) FROM agent_events e
                 JOIN agent_inputs i ON i.agent_id=e.agent_id AND i.id=e.agent_input_id
                 WHERE i.project_id=$1 AND i.agent_id=$2 AND i.input_kind='control'
                   AND i.state='resolved' AND i.control_type='cancel_current'),0)::bigint AS stop_sequence,
       coalesce((SELECT e.sequence FROM agent_events e WHERE e.agent_id=$2 AND e.event_kind='model_output'
                 ORDER BY e.sequence DESC LIMIT 1),0)::bigint AS output_sequence,
       coalesce((SELECT t.id FROM agent_turns t WHERE t.agent_id=$2 ORDER BY t.turn_sequence DESC LIMIT 1),
                 '00000000-0000-0000-0000-000000000000'::uuid)::uuid AS turn_id;

-- name: LoadExecutionFamily :one
SELECT root_agent_id,parent_agent_id FROM agents WHERE project_id=$1 AND id=$2;
