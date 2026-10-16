-- +goose Up

-- Machine file tools run as processes whose execution spec carries a file
-- operation payload.
ALTER TABLE processes
    DROP CONSTRAINT processes_execution_check,
    ADD CONSTRAINT processes_execution_check CHECK (COALESCE(
        jsonb_typeof(execution_spec) = 'object' AND (
            (execution_spec->>'kind' = 'shell'
                AND jsonb_typeof(execution_spec->'shell') = 'object'
                AND NOT execution_spec ? 'file_transfer'
                AND NOT execution_spec ? 'file'
                AND jsonb_typeof(execution_spec->'shell'->'command') = 'string'
                AND execution_spec->'shell'->>'command' <> ''
                AND jsonb_typeof(execution_spec->'shell'->'shell_selector') = 'string'
                AND execution_spec->'shell'->>'shell_selector' <> ''
                AND execution_spec->'shell'->>'io_mode' IN ('pipe', 'pty'))
            OR (execution_spec->>'kind' = 'file_transfer'
                AND jsonb_typeof(execution_spec->'file_transfer') = 'object'
                AND NOT execution_spec ? 'shell'
                AND NOT execution_spec ? 'file')
            OR (execution_spec->>'kind' IN ('file_read', 'file_write', 'file_list', 'file_search')
                AND jsonb_typeof(execution_spec->'file') = 'object'
                AND jsonb_typeof(execution_spec->'file'->'path') = 'string'
                AND NOT execution_spec ? 'shell'
                AND NOT execution_spec ? 'file_transfer')
        ), false
    ));
