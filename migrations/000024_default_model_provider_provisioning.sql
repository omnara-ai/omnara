-- +goose Up

CREATE TABLE default_model_provider_provisioning_jobs (
    organization_id uuid PRIMARY KEY REFERENCES orgs(id) ON DELETE CASCADE,
    creator_user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider_template jsonb NOT NULL,
    attempt_count integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    claim_token uuid,
    claim_expires_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    CHECK (attempt_count >= 0),
    CHECK (jsonb_typeof(provider_template) = 'object'),
    CHECK ((claim_token IS NULL) = (claim_expires_at IS NULL))
);

CREATE INDEX default_model_provider_provisioning_jobs_due_idx
    ON default_model_provider_provisioning_jobs(next_attempt_at, organization_id);
