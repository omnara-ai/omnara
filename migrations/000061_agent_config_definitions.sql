-- +goose Up

-- Held from the collision check through the pointer update so a config
-- committed mid-migration cannot be left without a definition.
LOCK TABLE agent_configs IN ACCESS EXCLUSIVE MODE;

-- Versions that compile to the same thing share one definition per project.
CREATE TABLE agent_config_definitions (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id uuid NOT NULL REFERENCES orgs(id),
    project_id uuid NOT NULL,
    configured_model_id uuid NOT NULL,
    compiled_definition jsonb NOT NULL,
    effective_definition_hash text NOT NULL,
    created_at timestamptz NOT NULL,
    CHECK (effective_definition_hash <> ''),
    CHECK (jsonb_typeof(compiled_definition) = 'object'),
    FOREIGN KEY (org_id, project_id) REFERENCES projects(org_id, id),
    FOREIGN KEY (org_id, configured_model_id) REFERENCES configured_models(org_id, id),
    UNIQUE (project_id, id),
    UNIQUE (project_id, effective_definition_hash)
);

-- +goose StatementBegin
DO $$
DECLARE
    conflict record;
BEGIN
    SELECT project_id, effective_definition_hash
    INTO conflict
    FROM agent_configs
    GROUP BY project_id, effective_definition_hash
    HAVING count(DISTINCT compiled_definition) > 1
        OR count(DISTINCT configured_model_id) > 1
        OR count(DISTINCT org_id) > 1
    LIMIT 1;
    IF FOUND THEN
        RAISE EXCEPTION 'agent configs in project % share effective definition hash % but differ in compiled content',
            conflict.project_id, conflict.effective_definition_hash;
    END IF;
END;
$$;
-- +goose StatementEnd

INSERT INTO agent_config_definitions(
    org_id, project_id, configured_model_id, compiled_definition,
    effective_definition_hash, created_at
)
SELECT DISTINCT ON (project_id, effective_definition_hash)
       org_id, project_id, configured_model_id, compiled_definition,
       effective_definition_hash, created_at
FROM agent_configs
ORDER BY project_id, effective_definition_hash, created_at, id;

ALTER TABLE agent_configs ADD COLUMN definition_id uuid;

ALTER TABLE agent_configs DISABLE TRIGGER agent_configs_immutable;

UPDATE agent_configs config
SET definition_id = definition.id
FROM agent_config_definitions definition
WHERE definition.project_id = config.project_id
  AND definition.effective_definition_hash = config.effective_definition_hash;

ALTER TABLE agent_configs ENABLE TRIGGER agent_configs_immutable;

-- Dropping effective_definition_hash also drops the old unique key. Versions
-- are intentionally not unique: rewrites may repoint two of them at one definition.
ALTER TABLE agent_configs
    ALTER COLUMN definition_id SET NOT NULL,
    ADD CONSTRAINT agent_configs_definition_fkey
        FOREIGN KEY (project_id, definition_id) REFERENCES agent_config_definitions(project_id, id),
    DROP COLUMN compiled_definition,
    DROP COLUMN effective_definition_hash,
    DROP COLUMN configured_model_id;

ALTER TABLE agent_configs
    ADD COLUMN source_revision integer NOT NULL DEFAULT 1 CHECK (source_revision >= 1);

ALTER TABLE agent_configs ALTER COLUMN source_revision DROP DEFAULT;

CREATE INDEX agent_configs_definition_source_idx
    ON agent_configs(project_id, definition_id, (coalesce(source_hash, '')));

-- +goose StatementBegin
CREATE FUNCTION reject_agent_config_definition_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'agent_config_definitions are immutable'
        USING ERRCODE = '25006';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER agent_config_definitions_immutable
    BEFORE UPDATE OR DELETE
    ON agent_config_definitions
    FOR EACH ROW
    EXECUTE FUNCTION reject_agent_config_definition_mutation();

-- Only definition_id may change, and only under
-- SET LOCAL omnara.agent_config_definition_rewrite = 'on'.
-- +goose StatementBegin
CREATE FUNCTION reject_agent_config_version_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'UPDATE'
       AND coalesce(current_setting('omnara.agent_config_definition_rewrite', true), '') = 'on'
       AND (to_jsonb(NEW) - 'definition_id') = (to_jsonb(OLD) - 'definition_id') THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'agent_configs are immutable'
        USING ERRCODE = '25006';
END;
$$;
-- +goose StatementEnd

DROP TRIGGER agent_configs_immutable ON agent_configs;

CREATE TRIGGER agent_configs_immutable
    BEFORE UPDATE OR DELETE
    ON agent_configs
    FOR EACH ROW
    EXECUTE FUNCTION reject_agent_config_version_mutation();

DROP FUNCTION reject_agent_config_mutation();
