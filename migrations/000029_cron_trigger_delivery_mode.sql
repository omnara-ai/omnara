-- +goose Up

ALTER TABLE cron_triggers
    ADD COLUMN delivery_mode text NOT NULL DEFAULT 'queued',
    ADD CONSTRAINT cron_triggers_delivery_mode_check CHECK (delivery_mode IN ('queued', 'steering')),
    ADD CONSTRAINT cron_triggers_profile_delivery_mode_check CHECK (
        agent_profile_id IS NULL OR delivery_mode = 'queued'
    );
