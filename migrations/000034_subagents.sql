-- +goose Up

ALTER TABLE agents
    ADD COLUMN parent_agent_id uuid,
    ADD COLUMN subagent_key text NOT NULL DEFAULT '',
    ADD COLUMN archive_after_idle_minutes integer,
    ADD CONSTRAINT agents_parent_agent_fk
        FOREIGN KEY (project_id, parent_agent_id) REFERENCES agents(project_id, id),
    ADD CONSTRAINT agents_subagent_key_check
        CHECK ((parent_agent_id IS NULL) = (subagent_key = '')),
    ADD CONSTRAINT agents_archive_after_idle_minutes_check
        CHECK (archive_after_idle_minutes IS NULL OR archive_after_idle_minutes >= 1),
    ADD CONSTRAINT agents_not_own_parent_check
        CHECK (parent_agent_id IS NULL OR parent_agent_id <> id);

CREATE INDEX agents_parent_agent_idx
    ON agents(project_id, parent_agent_id, created_at, id)
    WHERE parent_agent_id IS NOT NULL;

CREATE INDEX agents_idle_archive_candidates_idx
    ON agents(created_at, id)
    WHERE state = 'active'
      AND archive_after_idle_minutes IS NOT NULL;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION agents_reject_identity_change()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.id IS DISTINCT FROM NEW.id
       OR OLD.org_id IS DISTINCT FROM NEW.org_id
       OR OLD.project_id IS DISTINCT FROM NEW.project_id
       OR OLD.agent_profile_id IS DISTINCT FROM NEW.agent_profile_id
       OR OLD.parent_agent_id IS DISTINCT FROM NEW.parent_agent_id
       OR OLD.subagent_key IS DISTINCT FROM NEW.subagent_key
       OR OLD.idempotency_key IS DISTINCT FROM NEW.idempotency_key
       OR OLD.created_at IS DISTINCT FROM NEW.created_at THEN
        RAISE EXCEPTION 'agent identity is immutable'
            USING ERRCODE = '25006';
    END IF;

    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER agents_identity_immutable ON agents;

CREATE TRIGGER agents_identity_immutable
BEFORE UPDATE OF id, org_id, project_id, agent_profile_id, parent_agent_id,
    subagent_key, idempotency_key, created_at ON agents
FOR EACH ROW EXECUTE FUNCTION agents_reject_identity_change();
