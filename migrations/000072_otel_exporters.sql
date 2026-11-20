-- +goose Up

CREATE TABLE otel_exporters (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id uuid NOT NULL REFERENCES orgs(id),
    project_id uuid NOT NULL,
    agent_id uuid,
    endpoint text NOT NULL CHECK (octet_length(endpoint) BETWEEN 1 AND 2048),
    variant text NOT NULL DEFAULT 'otel' CHECK (variant IN ('otel', 'langfuse', 'braintrust', 'langsmith')),
    encoding text NOT NULL DEFAULT 'protobuf' CHECK (encoding IN ('json', 'protobuf')),
    auth_kind text NOT NULL DEFAULT 'none' CHECK (auth_kind IN ('none', 'bearer_token', 'api_key_header', 'basic')),
    auth_options jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(auth_options) = 'object'),
    credential_secret_id uuid,
    headers jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(headers) = 'object'),
    secret_headers jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(secret_headers) = 'object'),
    sample_percent numeric NOT NULL DEFAULT 100 CHECK (sample_percent BETWEEN 0 AND 100),
    created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    deleted_at timestamptz,
    UNIQUE (org_id, id),
    FOREIGN KEY (org_id, project_id) REFERENCES projects(org_id, id),
    FOREIGN KEY (project_id, agent_id) REFERENCES agents(project_id, id),
    FOREIGN KEY (org_id, credential_secret_id) REFERENCES secrets(org_id, id),
    CONSTRAINT otel_exporters_auth_credential_check CHECK ((auth_kind = 'none') = (credential_secret_id IS NULL))
);

CREATE UNIQUE INDEX otel_exporters_active_scope_endpoint_idx
    ON otel_exporters(org_id, project_id, agent_id, endpoint)
    NULLS NOT DISTINCT WHERE deleted_at IS NULL;

CREATE INDEX otel_exporters_org_created_idx ON otel_exporters(org_id, created_at, id);

CREATE INDEX otel_exporters_credential_secret_idx
    ON otel_exporters(org_id, credential_secret_id)
    WHERE credential_secret_id IS NOT NULL;

-- +goose StatementBegin
CREATE FUNCTION otel_trace_sampled(trace_id uuid, sample_percent numeric)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
    SELECT ('x' || right(replace(trace_id::text, '-', ''), 8))::bit(32)::bigint::numeric * 100 / 4294967296 < sample_percent;
$$;
-- +goose StatementEnd

CREATE TABLE otel_export_deliveries (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id uuid NOT NULL REFERENCES orgs(id),
    exporter_id uuid NOT NULL,
    agent_id uuid NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    turn_id uuid NOT NULL,
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    next_attempt_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    claim_token uuid,
    claim_expires_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    FOREIGN KEY (org_id, exporter_id) REFERENCES otel_exporters(org_id, id),
    FOREIGN KEY (agent_id, turn_id) REFERENCES agent_turns(agent_id, id),
    UNIQUE (exporter_id, turn_id),
    CHECK ((claim_token IS NULL) = (claim_expires_at IS NULL))
);

CREATE INDEX otel_export_deliveries_due_idx ON otel_export_deliveries(next_attempt_at, id);
CREATE INDEX otel_export_deliveries_agent_idx ON otel_export_deliveries(agent_id);
CREATE INDEX otel_export_deliveries_created_idx ON otel_export_deliveries(created_at);
CREATE INDEX otel_export_deliveries_claimed_idx ON otel_export_deliveries(claim_expires_at, org_id)
    WHERE claim_expires_at IS NOT NULL;

ALTER TABLE agent_inputs
    ADD COLUMN source_link_kind text,
    ADD COLUMN source_tool_call_id uuid,
    ADD COLUMN source_model_output_id uuid;

ALTER TABLE agent_inputs
    ADD CONSTRAINT agent_inputs_source_link_kind_check
        CHECK (source_link_kind IS NULL OR source_link_kind IN ('spawned_by', 'message_from', 'result_from')),
    ADD CONSTRAINT agent_inputs_source_link_check CHECK (
        (source_link_kind IS NULL AND source_tool_call_id IS NULL AND source_model_output_id IS NULL)
        OR (source_link_kind IN ('spawned_by', 'message_from') AND source_tool_call_id IS NOT NULL AND source_model_output_id IS NULL)
        OR (source_link_kind = 'result_from' AND source_tool_call_id IS NULL AND source_model_output_id IS NOT NULL)
    );

CREATE INDEX agent_inputs_source_tool_call_idx
    ON agent_inputs(source_tool_call_id)
    WHERE source_tool_call_id IS NOT NULL;
