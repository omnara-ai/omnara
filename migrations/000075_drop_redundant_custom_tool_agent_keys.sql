-- +goose Up

-- Remove redundant keys after both projection replacements have committed.
-- This short metadata transaction still needs several table locks. Live traffic
-- can abort it with 40P01 (deadlock) or 55P03 (lock timeout); it is safe to rerun.
-- Quiesce relevant application mutations for a predictable rollout.
-- Keep view replacement and historical backfills outside this transaction.
LOCK TABLE agents, agent_interactions, tool_calls IN ACCESS EXCLUSIVE MODE;

ALTER TABLE agent_interactions DROP CONSTRAINT agent_interactions_agent_id_fkey;
ALTER TABLE tool_calls DROP CONSTRAINT tool_calls_agent_id_fkey;
