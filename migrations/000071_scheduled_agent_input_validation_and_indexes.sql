-- +goose NO TRANSACTION
-- +goose Up

ALTER TABLE agent_inputs VALIDATE CONSTRAINT agent_inputs_state_check;
ALTER TABLE agent_inputs VALIDATE CONSTRAINT agent_inputs_lifecycle;
ALTER TABLE agent_inputs VALIDATE CONSTRAINT agent_inputs_scheduled_shape;

DROP INDEX CONCURRENTLY IF EXISTS agent_inputs_scheduled_due_idx;

CREATE INDEX CONCURRENTLY agent_inputs_scheduled_due_idx
    ON agent_inputs(scheduled_for, id)
    WHERE state = 'scheduled';

DROP INDEX CONCURRENTLY IF EXISTS agent_inputs_scheduled_agent_idx;

CREATE INDEX CONCURRENTLY agent_inputs_scheduled_agent_idx
    ON agent_inputs(agent_id, scheduled_for, id)
    WHERE state = 'scheduled';
