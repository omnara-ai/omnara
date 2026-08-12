-- +goose Up

ALTER TABLE agents
    ADD COLUMN agent_profile_id uuid;

ALTER TABLE agents
    ADD CONSTRAINT agents_agent_profile_fk
        FOREIGN KEY (project_id, agent_profile_id)
        REFERENCES agent_profiles(project_id, id);

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
BEFORE UPDATE OF id, org_id, project_id, agent_profile_id, idempotency_key, created_at ON agents
FOR EACH ROW EXECUTE FUNCTION agents_reject_identity_change();

CREATE INDEX agents_active_project_agent_profile_created_idx
    ON agents(project_id, agent_profile_id, created_at DESC, id DESC)
    WHERE state = 'active' AND agent_profile_id IS NOT NULL;
