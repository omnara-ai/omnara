-- +goose Up

-- Apply the custom-tool recovery schema in one transaction: project-scoped
-- work, complete event routing context, and durable grant creation receipts.
-- This consolidates unreleased migrations 69-73. Development databases that
-- applied those earlier branch revisions must be recreated before using it.

LOCK TABLE agents, tool_calls, agent_interactions IN ACCESS EXCLUSIVE MODE;

ALTER TABLE tool_calls
    ADD COLUMN project_id uuid,
    DROP CONSTRAINT tool_calls_agent_id_fkey;

ALTER TABLE agent_interactions
    ADD COLUMN project_id uuid,
    DROP CONSTRAINT agent_interactions_agent_id_fkey;

UPDATE tool_calls tool_call
SET project_id = agent.project_id
FROM agents agent
WHERE agent.id = tool_call.agent_id;

ALTER TABLE agent_interactions DISABLE TRIGGER agent_interactions_transition_guard;

UPDATE agent_interactions interaction
SET project_id = agent.project_id
FROM agents agent
WHERE agent.id = interaction.agent_id;

ALTER TABLE agent_interactions ENABLE TRIGGER agent_interactions_transition_guard;

ALTER TABLE tool_calls
    ALTER COLUMN project_id SET NOT NULL,
    ADD FOREIGN KEY (project_id, agent_id) REFERENCES agents(project_id, id);

ALTER TABLE agent_interactions
    ALTER COLUMN project_id SET NOT NULL,
    ADD FOREIGN KEY (project_id, agent_id) REFERENCES agents(project_id, id);

DROP TRIGGER tool_call_facts_immutable ON tool_calls;

CREATE TRIGGER tool_call_facts_immutable
BEFORE UPDATE OF
    id, project_id, agent_id, model_output_id, provider_call_id,
    name, input, type, created_at
OR DELETE ON tool_calls
FOR EACH ROW EXECUTE FUNCTION prevent_tool_call_fact_mutation();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION enforce_agent_interaction_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'agent_interactions are immutable'
            USING ERRCODE = '25006';
    END IF;
    IF TG_OP = 'INSERT' THEN
        IF NEW.state <> 'open' OR NEW.presentation_receipt IS NOT NULL THEN
            RAISE EXCEPTION 'agent_interactions must be inserted in open state'
                USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;

    -- Record late confirmed sends after cancellation so cleanup can dismiss them.
    IF OLD.presentation_receipt IS NULL AND NEW.presentation_receipt IS NOT NULL
       AND NEW.destination IS NOT NULL
       AND (to_jsonb(OLD) - 'presentation_receipt') = (to_jsonb(NEW) - 'presentation_receipt') THEN
        RETURN NEW;
    END IF;

    IF OLD.state <> 'open' THEN
        RAISE EXCEPTION 'terminal agent_interaction is immutable'
            USING ERRCODE = '25006';
    END IF;
    IF NEW.state NOT IN ('resolved', 'canceled') THEN
        RAISE EXCEPTION 'agent_interaction must transition from open to a terminal state'
            USING ERRCODE = '25006';
    END IF;
    IF OLD.id IS DISTINCT FROM NEW.id
       OR OLD.project_id IS DISTINCT FROM NEW.project_id
       OR OLD.agent_id IS DISTINCT FROM NEW.agent_id
       OR OLD.tool_call_id IS DISTINCT FROM NEW.tool_call_id
       OR OLD.interaction_kind IS DISTINCT FROM NEW.interaction_kind
       OR OLD.request IS DISTINCT FROM NEW.request
       OR OLD.created_at IS DISTINCT FROM NEW.created_at
       OR OLD.destination IS DISTINCT FROM NEW.destination
       OR OLD.presentation_receipt IS DISTINCT FROM NEW.presentation_receipt THEN
        RAISE EXCEPTION 'agent_interaction lineage is immutable'
            USING ERRCODE = '25006';
    END IF;

    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE INDEX tool_calls_project_unfinished_idx
    ON tool_calls(project_id, created_at, id)
    WHERE state <> 'completed';

CREATE INDEX agent_interactions_project_open_idx
    ON agent_interactions(project_id, created_at, id)
    WHERE state = 'open';

CREATE OR REPLACE VIEW tool_call_read_projection AS
SELECT tool_call.id,
       tool_call.project_id,
       tool_call.agent_id,
       source_event.turn_id,
       source_event.id AS source_event_id,
       source_event.sequence AS source_event_sequence,
       model_output.model_call_context_id,
       tool_call.model_output_id,
       tool_call.provider_call_id,
       tool_call.name,
       tool_call.input,
       tool_call.type,
       tool_call.state,
       tool_call.runtime_lock_id,
       tool_call.created_at
FROM tool_calls tool_call
JOIN model_outputs model_output ON model_output.agent_id = tool_call.agent_id
  AND model_output.id = tool_call.model_output_id
JOIN agent_events source_event ON source_event.agent_id = tool_call.agent_id
  AND source_event.model_output_id = tool_call.model_output_id
  AND source_event.event_kind = 'model_output';

CREATE OR REPLACE VIEW agent_event_read_projection AS
SELECT event.id,
       agent.org_id,
       agent.project_id,
       event.agent_id,
       event.turn_id,
       turn.turn_sequence,
       event.is_opening_event,
       event.sequence,
       event.event_kind,
       input.input_kind,
       input.actor_id,
       input.idempotency_scope,
       input.input_idempotency_key,
       event.agent_input_id,
       input.control_type,
       input.target_interaction_id,
       input.agent_config_id,
       tool_result.tool_call_id,
       tool_result.outcome AS tool_outcome,
       model_output.model_call_context_id,
       model_output.stop_reason AS model_stop_reason,
       event.context_checkpoint_id,
       checkpoint.summarized_through_event_sequence,
       checkpoint.summary AS checkpoint_summary,
       block_projection.content_blocks::jsonb AS content_blocks,
       event.created_at,
       model_call.input_tokens_total,
       model_call.uncached_input_tokens,
       model_call.cache_read_input_tokens,
       model_call.cache_write_input_tokens,
       model_call.output_tokens_total,
       model_call.reasoning_output_tokens,
       model_call.provider_metadata,
       agent.agent_profile_id,
       agent.parent_agent_id,
       agent.root_agent_id
FROM agent_events event
JOIN agents agent
  ON agent.id = event.agent_id
JOIN agent_turns turn
  ON turn.agent_id = event.agent_id
 AND turn.id = event.turn_id
LEFT JOIN agent_inputs input
  ON input.agent_id = event.agent_id
 AND input.id = event.agent_input_id
LEFT JOIN tool_call_results tool_result
  ON tool_result.agent_id = event.agent_id
 AND tool_result.id = event.tool_call_result_id
LEFT JOIN model_outputs model_output
  ON model_output.agent_id = event.agent_id
 AND model_output.id = event.model_output_id
LEFT JOIN context_checkpoints checkpoint
  ON checkpoint.agent_id = event.agent_id
 AND checkpoint.id = event.context_checkpoint_id
LEFT JOIN model_call_contexts model_call
  ON model_call.agent_id = model_output.agent_id
 AND model_call.id = model_output.model_call_context_id
CROSS JOIN LATERAL (
  SELECT coalesce(jsonb_agg(
    (
      CASE
        WHEN block.block_kind = 'text' THEN jsonb_build_object('type', 'text', 'text', block.text_content)
        WHEN block.block_kind = 'structured_data' THEN jsonb_build_object('type', 'structured_data', 'value', block.structured_data)
        WHEN block.block_kind = 'artifact' THEN
          jsonb_build_object('type', 'media_ref', 'artifact_id', block.artifact_id) ||
          CASE
            WHEN block.exclude_from_model_context THEN jsonb_build_object('exclude_from_model_context', true)
            ELSE '{}'::jsonb
          END
        WHEN block.block_kind = 'reasoning' THEN jsonb_build_object('type', 'reasoning', 'text', block.text_content)
        WHEN block.block_kind = 'error' THEN jsonb_build_object('type', 'error', 'text', block.text_content)
        WHEN block.block_kind = 'tool_call' THEN
          jsonb_build_object(
            'type', 'tool_call',
            'tool_call_id', block.tool_call_id,
            'tool_type', tool_block_call.type,
            'name', tool_block_call.name,
            'input', tool_block_call.input
          )
        ELSE NULL
      END
    ) || CASE
      WHEN block.metadata = '{}'::jsonb THEN '{}'::jsonb
      ELSE jsonb_build_object('metadata', block.metadata)
    END
    ORDER BY block.ordinal, block.id
  ) FILTER (WHERE block.id IS NOT NULL AND block.block_kind IN ('text', 'structured_data', 'artifact', 'reasoning', 'tool_call', 'error')), '[]'::jsonb) AS content_blocks
  FROM content_blocks block
  LEFT JOIN tool_calls tool_block_call
    ON tool_block_call.agent_id = block.agent_id
   AND tool_block_call.id = block.tool_call_id
  WHERE block.agent_id = event.agent_id
    AND (
      block.owner_agent_input_id = event.agent_input_id
      OR block.owner_model_output_id = event.model_output_id
      OR block.owner_tool_call_result_id = event.tool_call_result_id
    )
) block_projection;

-- Grants remain hard-deleted authorization edges. Creation receipts retain consumed
-- keys independently, including after revocation and lifecycle cleanup.

ALTER TABLE project_machine_grants ADD UNIQUE (project_id, id);

CREATE TABLE project_machine_grant_creations (
    org_id uuid NOT NULL,
    project_id uuid NOT NULL,
    idempotency_key text NOT NULL CHECK (idempotency_key <> ''),
    request_digest text NOT NULL CHECK (request_digest ~ '^[0-9a-f]{64}$'),
    grant_id uuid,
    created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    PRIMARY KEY (project_id, idempotency_key),
    UNIQUE (project_id, grant_id),
    FOREIGN KEY (org_id, project_id) REFERENCES projects(org_id, id),
    FOREIGN KEY (project_id, grant_id) REFERENCES project_machine_grants(project_id, id)
        ON DELETE SET NULL (grant_id)
);

-- Original create bodies were not retained before this migration. Backfill the
-- current normalized grant, matching the old replay comparison at this boundary.
INSERT INTO project_machine_grant_creations(org_id, project_id, idempotency_key, request_digest, grant_id, created_at)
SELECT org_id, project_id, idempotency_key,
       encode(sha256(convert_to(jsonb_build_object(
           'machine_id', machine_id,
           'description', description,
           'metadata', metadata
       )::text, 'UTF8')), 'hex'), id, created_at
FROM project_machine_grants
WHERE idempotency_key IS NOT NULL AND source_kind = 'explicit';

ALTER TABLE project_machine_grants DROP COLUMN idempotency_key;

CREATE TABLE project_machine_pool_grant_creations (
    org_id uuid NOT NULL,
    project_id uuid NOT NULL,
    idempotency_key text NOT NULL CHECK (idempotency_key <> ''),
    request_digest text NOT NULL CHECK (request_digest ~ '^[0-9a-f]{64}$'),
    grant_id uuid,
    created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    PRIMARY KEY (project_id, idempotency_key),
    UNIQUE (project_id, grant_id),
    FOREIGN KEY (org_id, project_id) REFERENCES projects(org_id, id),
    FOREIGN KEY (project_id, grant_id) REFERENCES project_machine_pool_grants(project_id, id)
        ON DELETE SET NULL (grant_id)
);

-- Original create bodies were not retained before this migration. Backfill the
-- current normalized grant, matching the old replay comparison at this boundary.
INSERT INTO project_machine_pool_grant_creations(org_id, project_id, idempotency_key, request_digest, grant_id, created_at)
SELECT org_id, project_id, idempotency_key,
       encode(sha256(convert_to(jsonb_build_object(
           'machine_pool_id', machine_pool_id,
           'description', description,
           'default_machine_cpu', default_machine_cpu,
           'default_machine_memory_mb', default_machine_memory_mb,
           'default_machine_env_overlay', default_machine_env_overlay,
           'default_machine_secret_env_overlay', default_machine_secret_env_overlay,
           'default_machine_provider_options_overlay', default_machine_provider_options_overlay,
           'default_cwd', default_cwd,
           'max_total_machines', max_total_machines,
           'max_total_cpu', max_total_cpu,
           'max_total_memory_mb', max_total_memory_mb,
           'min_machine_cpu', min_machine_cpu,
           'min_machine_memory_mb', min_machine_memory_mb,
           'max_machine_cpu', max_machine_cpu,
           'max_machine_memory_mb', max_machine_memory_mb,
           'delete_after_idle_minutes', delete_after_idle_minutes,
           'metadata', metadata
       )::text, 'UTF8')), 'hex'), id, created_at
FROM project_machine_pool_grants
WHERE idempotency_key IS NOT NULL;

ALTER TABLE project_machine_pool_grants DROP COLUMN idempotency_key;


-- Durable provider header references use the same authority boundary as credentials.
ALTER TABLE model_provider_configs ADD UNIQUE (org_id, id, management_kind);
CREATE TABLE model_provider_header_secrets (
    org_id uuid NOT NULL,
    provider_config_id uuid NOT NULL,
    header_name text NOT NULL CHECK (header_name <> ''),
    secret_id uuid NOT NULL,
    management_kind text NOT NULL,
    PRIMARY KEY (org_id, provider_config_id, header_name),
    FOREIGN KEY (org_id, provider_config_id, management_kind)
        REFERENCES model_provider_configs(org_id, id, management_kind),
    FOREIGN KEY (org_id, secret_id, management_kind)
        REFERENCES secrets(org_id, id, management_kind)
);
CREATE INDEX model_provider_header_secrets_secret_idx
    ON model_provider_header_secrets(org_id, secret_id);
-- Preserve configured references even when their secret was already soft-deleted:
-- resolution must fail closed until the provider configuration is repaired.
INSERT INTO model_provider_header_secrets(org_id, provider_config_id, header_name, secret_id, management_kind)
SELECT config.org_id, config.id, header.key, header.value::uuid, config.management_kind
FROM model_provider_configs config
CROSS JOIN LATERAL jsonb_each_text(config.secret_headers) header
WHERE config.deleted_at IS NULL;
ALTER TABLE model_provider_configs DROP COLUMN secret_headers;

-- Every live consumer reference has an indexed reverse lookup for deletion guards.
CREATE INDEX model_provider_configs_credential_secret_idx
    ON model_provider_configs(org_id, credential_secret_id) WHERE credential_secret_id IS NOT NULL;
CREATE INDEX machine_pools_provider_auth_secret_idx
    ON machine_pools(org_id, provider_auth_secret_id) WHERE provider_auth_secret_id IS NOT NULL;
CREATE INDEX event_webhooks_signing_secret_idx
    ON event_webhooks(org_id, signing_secret_id) WHERE signing_secret_id IS NOT NULL;

-- Deleted scopes cannot receive deliveries or retain ownership of signing secrets.
UPDATE event_webhooks webhook
SET deleted_at = coalesce(webhook.deleted_at, statement_timestamp()),
    signing_secret_id = NULL, updated_at = statement_timestamp()
WHERE webhook.deleted_at IS NOT NULL OR EXISTS (
    SELECT 1 FROM projects project JOIN orgs org ON org.id = project.org_id
    WHERE project.id = webhook.project_id AND (project.deleted_at IS NOT NULL OR org.deleted_at IS NOT NULL)
);
ALTER TABLE event_webhooks ADD CONSTRAINT event_webhooks_deleted_signing_secret_check
    CHECK (deleted_at IS NULL OR signing_secret_id IS NULL);
