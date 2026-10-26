-- +goose Up

CREATE TABLE event_webhooks (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id uuid NOT NULL REFERENCES orgs(id),
    project_id uuid NOT NULL,
    agent_profile_id uuid,
    agent_id uuid,
    url text NOT NULL CHECK (octet_length(url) BETWEEN 1 AND 2048),
    event_types text[] NOT NULL CHECK (
        cardinality(event_types) > 0
        AND event_types <@ ARRAY[
            'agent_input', 'model_output', 'tool_result', 'context_checkpoint', 'tool_call_update'
        ]::text[]
    ),
    signing_secret_id uuid,
    created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    deleted_at timestamptz,
    UNIQUE (org_id, id),
    FOREIGN KEY (org_id, project_id) REFERENCES projects(org_id, id),
    FOREIGN KEY (project_id, agent_profile_id) REFERENCES agent_profiles(project_id, id),
    FOREIGN KEY (project_id, agent_id) REFERENCES agents(project_id, id),
    FOREIGN KEY (org_id, signing_secret_id) REFERENCES secrets(org_id, id),
    CONSTRAINT event_webhooks_scope_check CHECK (num_nonnulls(agent_profile_id, agent_id) <= 1)
);

CREATE UNIQUE INDEX event_webhooks_active_scope_url_idx
    ON event_webhooks(project_id, agent_profile_id, agent_id, url)
    NULLS NOT DISTINCT WHERE deleted_at IS NULL;

DELETE FROM event_webhook_deliveries;

ALTER TABLE event_webhook_deliveries
    ADD COLUMN webhook_id uuid NOT NULL,
    ADD FOREIGN KEY (org_id, webhook_id) REFERENCES event_webhooks(org_id, id);
