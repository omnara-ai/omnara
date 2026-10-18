-- +goose Up

CREATE OR REPLACE VIEW tool_call_read_projection AS
SELECT tool_call.id,
       agent.project_id,
       tool_call.agent_id,
       context.turn_id,
       source_event.id AS source_event_id,
       source_event.sequence AS source_event_sequence,
       model_output.model_call_context_id,
       tool_call.model_output_id,
       tool_call.provider_call_id,
       tool_call.name,
       tool_call.input,
       tool_call.type,
       tool_call.state,
       tool_call.runtime_lock_id,
       tool_call.created_at
FROM tool_calls tool_call
JOIN agents agent ON agent.id = tool_call.agent_id
JOIN model_outputs model_output ON model_output.agent_id = tool_call.agent_id
  AND model_output.id = tool_call.model_output_id
JOIN model_call_contexts context ON context.agent_id = model_output.agent_id
  AND context.id = model_output.model_call_context_id
JOIN agent_events source_event ON source_event.agent_id = tool_call.agent_id
  AND source_event.model_output_id = tool_call.model_output_id
  AND source_event.event_kind = 'model_output';
