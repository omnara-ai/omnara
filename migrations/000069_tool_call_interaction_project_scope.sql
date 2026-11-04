-- +goose Up

LOCK TABLE agents, tool_calls, agent_interactions IN ACCESS EXCLUSIVE MODE;

ALTER TABLE tool_calls
    ADD COLUMN project_id uuid,
    DROP CONSTRAINT tool_calls_agent_id_fkey;

ALTER TABLE agent_interactions
    ADD COLUMN project_id uuid,
    DROP CONSTRAINT agent_interactions_agent_id_fkey;
