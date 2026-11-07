-- +goose Up

-- Commit the interaction-table expansion before requesting this table's lock.
-- Do not combine the two ALTERs: old readers and writers acquire them in both orders.

ALTER TABLE tool_calls ADD COLUMN project_id uuid;
