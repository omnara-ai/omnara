-- +goose NO TRANSACTION
-- +goose Up

DROP INDEX CONCURRENTLY IF EXISTS agent_events_checkpoint_sequence_idx;

CREATE INDEX CONCURRENTLY agent_events_checkpoint_sequence_idx ON agent_events(agent_id, sequence)
    WHERE event_kind = 'context_checkpoint';
