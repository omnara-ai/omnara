-- +goose Up

-- Add one nullable column per transaction. Old application transactions acquire
-- agents and these child tables in conflicting orders; keep this single-table
-- expansion separate from tool_calls and all other table DDL.
-- These are unreleased branch migrations. Recreate development databases that
-- already applied an earlier version of migrations 69 onward.

ALTER TABLE agent_interactions ADD COLUMN project_id uuid;
