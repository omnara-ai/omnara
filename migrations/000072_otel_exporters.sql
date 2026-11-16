-- +goose Up

-- An OTLP/HTTP trace destination. Scope narrows from the organization
-- (project_id and agent_id NULL) to one project, to one agent.
CREATE TABLE otel_exporters (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id uuid NOT NULL REFERENCES orgs(id),
    project_id uuid,
    agent_id uuid,
    endpoint text NOT NULL CHECK (octet_length(endpoint) BETWEEN 1 AND 2048),
    variant text NOT NULL DEFAULT 'otel' CHECK (variant IN ('otel', 'langfuse', 'braintrust', 'langsmith')),
    encoding text NOT NULL DEFAULT 'json' CHECK (encoding IN ('json', 'protobuf')),
    headers jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(headers) = 'object'),
    secret_headers jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(secret_headers) = 'object'),
    sample_percent numeric(5, 2) NOT NULL DEFAULT 100 CHECK (sample_percent BETWEEN 0 AND 100),
    created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    deleted_at timestamptz,
    UNIQUE (org_id, id),
    FOREIGN KEY (org_id, project_id) REFERENCES projects(org_id, id),
    FOREIGN KEY (project_id, agent_id) REFERENCES agents(project_id, id),
    CONSTRAINT otel_exporters_scope_check CHECK (agent_id IS NULL OR project_id IS NOT NULL)
);

CREATE UNIQUE INDEX otel_exporters_active_scope_endpoint_idx
    ON otel_exporters(org_id, project_id, agent_id, endpoint)
    NULLS NOT DISTINCT WHERE deleted_at IS NULL;

CREATE INDEX otel_exporters_org_created_idx ON otel_exporters(org_id, created_at, id);

-- Head sampling keyed on the trace (turn) ID, so every span of one trace
-- gets the same decision for one exporter. Buckets are the low 32 bits of
-- the ID modulo 10000, so a percentage is honored to two decimal places.
-- +goose StatementBegin
CREATE FUNCTION otel_trace_sampled(trace_id uuid, sample_percent numeric)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
    SELECT (('x' || right(replace(trace_id::text, '-', ''), 8))::bit(32)::bigint % 10000) < sample_percent * 100;
$$;
-- +goose StatementEnd

-- One row per (exporter, turn) whose trace still has to be exported. A turn is
-- one trace. The worker claims many rows for one exporter at a time and sends
-- their traces as a single OTLP request.
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
CREATE INDEX otel_export_deliveries_exporter_due_idx ON otel_export_deliveries(exporter_id, next_attempt_at, id);
CREATE INDEX otel_export_deliveries_agent_idx ON otel_export_deliveries(agent_id);
CREATE INDEX otel_export_deliveries_created_idx ON otel_export_deliveries(created_at);
CREATE INDEX otel_export_deliveries_claim_idx ON otel_export_deliveries(claim_token) WHERE claim_token IS NOT NULL;
CREATE INDEX otel_export_deliveries_claimed_idx ON otel_export_deliveries(claim_expires_at, org_id)
    WHERE claim_expires_at IS NOT NULL;
