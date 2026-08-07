-- +goose Up
ALTER TABLE machines
ADD COLUMN failure_report jsonb,
ADD CONSTRAINT machines_failure_report_check CHECK (
    failure_report IS NULL
    OR jsonb_typeof(failure_report) = 'object'
);
