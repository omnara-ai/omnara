-- +goose Up

ALTER TABLE processes
    ADD COLUMN notify_on_exit boolean NOT NULL DEFAULT false;
