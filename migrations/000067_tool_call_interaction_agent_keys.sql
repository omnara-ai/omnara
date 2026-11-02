-- +goose Up

LOCK TABLE agents, tool_calls, agent_interactions IN ACCESS EXCLUSIVE MODE;

ALTER TABLE tool_calls
    ADD FOREIGN KEY (project_id, agent_id) REFERENCES agents(project_id, id),
    DROP CONSTRAINT tool_calls_agent_id_fkey;

ALTER TABLE agent_interactions
    ADD FOREIGN KEY (project_id, agent_id) REFERENCES agents(project_id, id),
    DROP CONSTRAINT agent_interactions_agent_id_fkey;
