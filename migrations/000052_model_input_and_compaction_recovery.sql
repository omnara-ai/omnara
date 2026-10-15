-- +goose Up

ALTER TABLE model_call_contexts
    ADD COLUMN input_fingerprint text,
    ADD CONSTRAINT model_call_contexts_input_fingerprint CHECK (
        input_fingerprint IS NULL OR (
            operation_kind = 'normal' AND state = 'succeeded'
            AND coalesce(input_tokens_total, 0) > 0
            AND input_fingerprint ~ '^[0-9a-f]{64}$'
        )
    );
