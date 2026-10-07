-- +goose Up

ALTER TABLE event_webhook_deliveries
    ADD COLUMN interaction_update jsonb,
    ADD CHECK (interaction_update IS NULL OR
        (tool_call_id IS NOT NULL AND jsonb_typeof(interaction_update) = 'object'));
