-- +goose Up

ALTER TABLE model_provider_configs DROP CONSTRAINT model_provider_configs_api_variant_check;
ALTER TABLE model_provider_configs ADD CONSTRAINT model_provider_configs_api_variant_check
    CHECK (api_variant IN ('default', 'openrouter', 'bedrock', 'openrouter-batch'));

-- One row per model call that is waiting on a provider batch. Its runtime lock
-- stays held (parked) while no worker goroutine is attached. runtime_lock_id is
-- deliberately not a foreign key: releasing a runtime lock is a hot path and
-- must not pay a referential check for a table most agents never use. Rows are
-- deleted when their model call finishes, and rows whose lock is gone are never
-- resumed and are purged by the runtime lock reaper.
CREATE TABLE model_batch_requests (
    id uuid PRIMARY KEY,
    agent_id uuid NOT NULL,
    runtime_lock_id uuid NOT NULL,
    model_call_context_id uuid NOT NULL,
    turn_id uuid NOT NULL,
    opening_input_ids uuid[] NOT NULL,
    opening_event_sequence bigint NOT NULL,
    callback_token_hash bytea NOT NULL,
    custom_id text NOT NULL,
    input_fingerprint text NOT NULL DEFAULT '',
    provider_batch_id text NOT NULL DEFAULT '',
    state text NOT NULL DEFAULT 'submitting',
    wake_requested_at timestamptz,
    resume_at timestamptz,
    deadline_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    UNIQUE (runtime_lock_id),
    CHECK (state IN ('submitting', 'parked', 'collecting')),
    CHECK (state = 'submitting' OR provider_batch_id <> ''),
    CHECK (state <> 'parked' OR resume_at IS NOT NULL),
    CHECK (cardinality(opening_input_ids) > 0 AND opening_event_sequence > 0)
);

CREATE INDEX model_batch_requests_resume_idx
    ON model_batch_requests(resume_at ASC, id ASC)
    WHERE state = 'parked';
