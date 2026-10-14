-- +goose Up

ALTER TABLE memory_stores
    ADD COLUMN files_removed_at timestamptz
        CHECK (files_removed_at IS NULL OR deleted_at IS NOT NULL);
