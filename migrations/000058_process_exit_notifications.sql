-- +goose Up

ALTER TABLE processes
    ADD COLUMN notify_on_exit boolean NOT NULL DEFAULT false,
    ADD COLUMN notify_match text NOT NULL DEFAULT '';
