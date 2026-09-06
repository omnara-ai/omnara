-- +goose Up

ALTER TABLE configured_model_revisions
    ALTER COLUMN max_output_tokens DROP NOT NULL,
    ADD CONSTRAINT configured_model_revisions_minimum_context
    CHECK (context_window_tokens >= 2),
    ADD CONSTRAINT configured_model_revisions_default_output_within_context
    CHECK (default_max_output_tokens IS NULL OR default_max_output_tokens < context_window_tokens);
