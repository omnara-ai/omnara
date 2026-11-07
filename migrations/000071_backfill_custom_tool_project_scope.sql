-- +goose Up

-- Completed tool-call facts are immutable. Commit this historical rewrite before
-- any blocking finalization work, so a later lock timeout does not repeat it.

UPDATE tool_calls tool_call
SET project_id = agent.project_id
FROM agents agent
WHERE agent.id = tool_call.agent_id
  AND tool_call.state = 'completed';
