-- +goose Up

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION enforce_model_call_context_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    receipt_columns constant text[] := ARRAY[
        'api_format', 'api_variant', 'provider_response_id',
        'input_tokens_total', 'uncached_input_tokens', 'cache_read_input_tokens',
        'cache_write_input_tokens', 'output_tokens_total', 'reasoning_output_tokens',
        'provider_reported_cost_usd', 'effective_cost_usd', 'effective_cost_kind'
    ];
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'model_call_contexts are immutable'
            USING ERRCODE = '25006';
    END IF;
    IF TG_OP = 'INSERT' THEN
        IF NEW.state <> 'started' THEN
            RAISE EXCEPTION 'model_call_contexts must be inserted in started state'
                USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;

    IF OLD.id IS DISTINCT FROM NEW.id
       OR OLD.org_id IS DISTINCT FROM NEW.org_id
       OR OLD.project_id IS DISTINCT FROM NEW.project_id
       OR OLD.agent_id IS DISTINCT FROM NEW.agent_id
       OR OLD.operation_kind IS DISTINCT FROM NEW.operation_kind
       OR OLD.attempt_number IS DISTINCT FROM NEW.attempt_number
       OR OLD.agent_config_id IS DISTINCT FROM NEW.agent_config_id
       OR OLD.configured_model_revision_id IS DISTINCT FROM NEW.configured_model_revision_id
       OR OLD.input_event_sequence IS DISTINCT FROM NEW.input_event_sequence
       OR OLD.source_event_sequence_end IS DISTINCT FROM NEW.source_event_sequence_end
       OR OLD.runtime_lock_id IS DISTINCT FROM NEW.runtime_lock_id
       OR OLD.created_at IS DISTINCT FROM NEW.created_at THEN
        RAISE EXCEPTION 'model_call_context identity and runtime ownership are immutable'
            USING ERRCODE = '25006';
    END IF;

    IF OLD.state IN ('failed', 'canceled')
       AND (to_jsonb(OLD) - receipt_columns) = (to_jsonb(NEW) - receipt_columns)
       AND (OLD.api_format = '' OR OLD.api_format = NEW.api_format)
       AND (OLD.api_variant = '' OR OLD.api_variant = NEW.api_variant)
       AND (OLD.provider_response_id = '' OR OLD.provider_response_id = NEW.provider_response_id)
       AND (OLD.input_tokens_total IS NULL OR OLD.input_tokens_total = NEW.input_tokens_total)
       AND (OLD.uncached_input_tokens IS NULL OR OLD.uncached_input_tokens = NEW.uncached_input_tokens)
       AND (OLD.cache_read_input_tokens IS NULL OR OLD.cache_read_input_tokens = NEW.cache_read_input_tokens)
       AND (OLD.cache_write_input_tokens IS NULL OR OLD.cache_write_input_tokens = NEW.cache_write_input_tokens)
       AND (OLD.output_tokens_total IS NULL OR OLD.output_tokens_total = NEW.output_tokens_total)
       AND (OLD.reasoning_output_tokens IS NULL OR OLD.reasoning_output_tokens = NEW.reasoning_output_tokens)
       AND (OLD.provider_reported_cost_usd IS NULL
            OR OLD.provider_reported_cost_usd = NEW.provider_reported_cost_usd)
       AND (OLD.effective_cost_usd IS NULL OR OLD.effective_cost_usd = NEW.effective_cost_usd)
       AND (OLD.effective_cost_kind IS NULL OR OLD.effective_cost_kind = NEW.effective_cost_kind) THEN
        RETURN NEW;
    END IF;

    IF OLD.state IN ('succeeded', 'failed', 'canceled') THEN
        RAISE EXCEPTION 'terminal model_call_contexts are immutable'
            USING ERRCODE = '25006';
    END IF;

    IF OLD.state <> 'started' OR NEW.state NOT IN ('succeeded', 'failed', 'canceled') THEN
        RAISE EXCEPTION 'invalid model_call_context transition % -> %', OLD.state, NEW.state
            USING ERRCODE = '23514';
    END IF;

    RETURN NEW;
END;
$$;
-- +goose StatementEnd

ALTER TABLE agent_runtime_locks
    ADD COLUMN inflight_model_call_context_id uuid,
    ADD COLUMN inflight_generation_id text,
    ADD CONSTRAINT agent_runtime_locks_inflight_generation_check CHECK (
        (inflight_model_call_context_id IS NULL) = (inflight_generation_id IS NULL)
        AND inflight_generation_id <> ''
    );
