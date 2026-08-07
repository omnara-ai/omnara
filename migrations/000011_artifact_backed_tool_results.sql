-- +goose Up

ALTER TABLE content_blocks
ADD COLUMN metadata jsonb NOT NULL DEFAULT '{}'::jsonb;

ALTER TABLE content_blocks
ADD CONSTRAINT content_blocks_metadata_object
CHECK (jsonb_typeof(metadata) = 'object');
