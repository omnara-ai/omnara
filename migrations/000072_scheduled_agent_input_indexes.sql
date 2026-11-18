-- +goose NO TRANSACTION
-- +goose Up

DROP INDEX CONCURRENTLY IF EXISTS agent_inputs_scheduled_due_idx;

CREATE INDEX CONCURRENTLY agent_inputs_scheduled_due_idx
    ON agent_inputs(scheduled_for, id)
    WHERE state = 'scheduled';

DROP INDEX CONCURRENTLY IF EXISTS agent_inputs_scheduled_agent_idx;

CREATE INDEX CONCURRENTLY agent_inputs_scheduled_agent_idx
    ON agent_inputs(agent_id, scheduled_for, id)
    WHERE state = 'scheduled';
