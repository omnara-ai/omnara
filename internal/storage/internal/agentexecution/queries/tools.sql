-- name: LoadExecutionBatch :one
SELECT EXISTS(SELECT 1 FROM tool_calls c WHERE c.agent_id=$1 AND c.model_output_id=$2) AS has_tools,
       EXISTS(SELECT 1 FROM tool_calls c WHERE c.agent_id=$1 AND c.model_output_id=$2 AND c.state<>'completed') AS incomplete,
       EXISTS(SELECT 1 FROM tool_calls c WHERE c.agent_id=$1 AND c.model_output_id=$2 AND
              (c.state='awaiting_authorization' OR (c.state='ready' AND c.type IN ('built_in','mcp')))) AS runnable;

-- name: LoadExecutionBatchCompletion :one
SELECT count(c.id)::bigint AS calls,count(r.id)::bigint AS results,count(e.id)::bigint AS events,
       coalesce(max(e.sequence),0)::bigint AS last_result_sequence,
       coalesce(max(r.completed_at),'epoch'::timestamptz)::timestamptz AS ready_at
FROM tool_calls c LEFT JOIN tool_call_results r ON r.agent_id=c.agent_id AND r.tool_call_id=c.id
LEFT JOIN agent_events e ON e.agent_id=r.agent_id AND e.tool_call_result_id=r.id
WHERE c.agent_id=$1 AND c.model_output_id=$2;
