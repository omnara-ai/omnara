-- +goose Up

CREATE TABLE event_webhook_deliveries (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id uuid NOT NULL REFERENCES orgs(id),
    agent_id uuid NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    event_sequence bigint,
    tool_call_id uuid,
    tool_state text,
    attempt_count integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    claim_token uuid,
    claim_expires_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    CHECK ((event_sequence IS NOT NULL AND event_sequence > 0 AND tool_call_id IS NULL AND tool_state IS NULL)
        OR (event_sequence IS NULL AND tool_call_id IS NOT NULL AND tool_state IS NOT NULL)),
    CHECK ((claim_token IS NULL) = (claim_expires_at IS NULL)),
    CHECK (attempt_count >= 0)
);

CREATE INDEX event_webhook_deliveries_due_idx ON event_webhook_deliveries(next_attempt_at, id);
CREATE INDEX event_webhook_deliveries_agent_idx ON event_webhook_deliveries(agent_id);
CREATE INDEX event_webhook_deliveries_created_idx ON event_webhook_deliveries(created_at);

CREATE INDEX event_webhook_deliveries_claimed_idx ON event_webhook_deliveries(claim_expires_at, org_id)
    WHERE claim_expires_at IS NOT NULL;
