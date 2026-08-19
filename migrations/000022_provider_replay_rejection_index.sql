-- +goose Up

CREATE INDEX model_call_contexts_replay_rejection_idx
    ON model_call_contexts(project_id, agent_id, input_event_sequence DESC)
    INCLUDE (org_id, configured_model_revision_id, api_format, api_variant)
    WHERE operation_kind = 'normal'
      AND state = 'failed'
      AND error_kind = 'replay_rejected';
