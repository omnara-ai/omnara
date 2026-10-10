-- +goose Up

-- +goose StatementBegin
CREATE FUNCTION agent_payload_created_at(
    p_project_id uuid, p_agent_id uuid, p_kind text, p_id uuid, p_admission boolean
)
RETURNS timestamptz
LANGUAGE plpgsql STABLE
AS $$
DECLARE result timestamptz;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM agents WHERE project_id = p_project_id AND id = p_agent_id) THEN
        RETURN NULL;
    END IF;
    CASE p_kind
        WHEN 'agent_input' THEN
            SELECT CASE WHEN p_admission THEN statement_timestamp() ELSE queued_at END INTO result
            FROM agent_inputs WHERE agent_id = p_agent_id AND id = p_id;
        WHEN 'model_output' THEN
            SELECT created_at INTO result FROM model_outputs WHERE agent_id = p_agent_id AND id = p_id;
        WHEN 'tool_call_result' THEN
            SELECT completed_at INTO result FROM tool_call_results WHERE agent_id = p_agent_id AND id = p_id;
        WHEN 'context_checkpoint' THEN
            SELECT created_at INTO result FROM context_checkpoints WHERE agent_id = p_agent_id AND id = p_id;
        ELSE RETURN NULL;
    END CASE;
    RETURN result;
END;
$$;
-- +goose StatementEnd
