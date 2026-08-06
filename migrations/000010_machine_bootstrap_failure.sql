-- +goose Up
ALTER TABLE machines
ADD COLUMN bootstrap_failure jsonb,
ADD CONSTRAINT machines_bootstrap_failure_check CHECK (
    bootstrap_failure IS NULL
    OR (
        source_kind = 'pool'
        AND jsonb_typeof(bootstrap_failure) = 'object'
    )
);
