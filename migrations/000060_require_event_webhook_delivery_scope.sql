-- +goose Up

ALTER TABLE event_webhook_deliveries ALTER COLUMN webhook_id SET NOT NULL;
