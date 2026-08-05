-- +goose Up

CREATE TABLE skills (
    id                 uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id             uuid NOT NULL REFERENCES orgs(id),
    owner_kind         text NOT NULL,
    owner_project_id   uuid,
    owner_user_id      uuid REFERENCES users(id),
    name               text NOT NULL,
    created_at         timestamptz NOT NULL,
    deleted_at         timestamptz,

    UNIQUE (org_id, id),
    FOREIGN KEY (org_id, owner_project_id) REFERENCES projects(org_id, id),
    CHECK (owner_kind IN ('org', 'project', 'user')),
    CHECK (name <> ''),
    CHECK (
        (owner_kind = 'org'     AND owner_project_id IS NULL AND owner_user_id IS NULL) OR
        (owner_kind = 'project' AND owner_project_id IS NOT NULL AND owner_user_id IS NULL) OR
        (owner_kind = 'user'    AND owner_project_id IS NULL AND owner_user_id IS NOT NULL)
    )
);

CREATE UNIQUE INDEX skills_org_owner_name_idx
    ON skills(org_id, name)
    WHERE owner_kind = 'org' AND deleted_at IS NULL;
CREATE UNIQUE INDEX skills_project_owner_name_idx
    ON skills(org_id, owner_project_id, name)
    WHERE owner_kind = 'project' AND deleted_at IS NULL;
CREATE UNIQUE INDEX skills_user_owner_name_idx
    ON skills(owner_user_id, org_id, name)
    WHERE owner_kind = 'user' AND deleted_at IS NULL;

CREATE INDEX skills_name_trgm_idx
    ON skills USING gin (name gin_trgm_ops)
    WHERE deleted_at IS NULL;

CREATE TABLE skill_revisions (
    id                 uuid PRIMARY KEY DEFAULT uuidv7(),
    skill_id           uuid NOT NULL,
    revision           integer NOT NULL,
    description        text NOT NULL,
    skill_md           text NOT NULL,
    archive_digest     text NOT NULL,
    created_at         timestamptz NOT NULL,
    deleted_at         timestamptz,

    UNIQUE (skill_id, revision),
    FOREIGN KEY (skill_id) REFERENCES skills(id),
    CHECK (revision > 0),
    CHECK (description <> ''),
    CHECK (skill_md <> ''),
    CHECK (archive_digest <> '')
);

-- +goose StatementBegin
CREATE FUNCTION enforce_skill_revision_mutation_policy()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        PERFORM 1
        FROM skills skill
        WHERE skill.id = NEW.skill_id
          AND skill.deleted_at IS NULL
        FOR SHARE;

        IF FOUND THEN
            RETURN NEW;
        END IF;

        RAISE EXCEPTION 'skill revisions require an active skill identity'
            USING ERRCODE = '23514';
    END IF;

    IF TG_OP = 'UPDATE'
       AND OLD.id IS NOT DISTINCT FROM NEW.id
       AND OLD.skill_id IS NOT DISTINCT FROM NEW.skill_id
       AND OLD.revision IS NOT DISTINCT FROM NEW.revision
       AND OLD.description IS NOT DISTINCT FROM NEW.description
       AND OLD.skill_md IS NOT DISTINCT FROM NEW.skill_md
       AND OLD.archive_digest IS NOT DISTINCT FROM NEW.archive_digest
       AND OLD.created_at IS NOT DISTINCT FROM NEW.created_at
       AND OLD.deleted_at IS NULL
       AND NEW.deleted_at IS NOT NULL THEN
        PERFORM 1
        FROM skills skill
        WHERE skill.id = OLD.skill_id
          AND skill.deleted_at IS NOT NULL
        FOR SHARE;

        IF FOUND THEN
            RETURN NEW;
        END IF;
    END IF;

    RAISE EXCEPTION 'skill revisions are immutable'
        USING ERRCODE = '25006';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER skill_revisions_mutation_policy
BEFORE INSERT OR UPDATE OR DELETE ON skill_revisions
FOR EACH ROW EXECUTE FUNCTION enforce_skill_revision_mutation_policy();

-- +goose StatementBegin
CREATE FUNCTION enforce_skill_revision_set()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.deleted_at IS NULL AND NOT EXISTS (
        SELECT 1
        FROM skill_revisions revision
        WHERE revision.skill_id = NEW.id
          AND revision.deleted_at IS NULL
    ) THEN
        RAISE EXCEPTION 'active skill identities require an active revision'
            USING ERRCODE = '23514';
    END IF;

    IF NEW.deleted_at IS NOT NULL AND EXISTS (
        SELECT 1
        FROM skill_revisions revision
        WHERE revision.skill_id = NEW.id
          AND revision.deleted_at IS NULL
    ) THEN
        RAISE EXCEPTION 'deleted skill identities cannot retain active revisions'
            USING ERRCODE = '23514';
    END IF;

    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER skills_revision_set_valid
AFTER INSERT OR UPDATE ON skills
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION enforce_skill_revision_set();

-- +goose StatementBegin
CREATE FUNCTION enforce_skill_identity_mutation_policy()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'UPDATE'
       AND OLD.id IS NOT DISTINCT FROM NEW.id
       AND OLD.org_id IS NOT DISTINCT FROM NEW.org_id
       AND OLD.owner_kind IS NOT DISTINCT FROM NEW.owner_kind
       AND OLD.owner_project_id IS NOT DISTINCT FROM NEW.owner_project_id
       AND OLD.owner_user_id IS NOT DISTINCT FROM NEW.owner_user_id
       AND OLD.name IS NOT DISTINCT FROM NEW.name
       AND OLD.created_at IS NOT DISTINCT FROM NEW.created_at
       AND OLD.deleted_at IS NULL
       AND NEW.deleted_at IS NOT NULL THEN
        RETURN NEW;
    END IF;

    RAISE EXCEPTION 'skill identities and ownership are immutable'
        USING ERRCODE = '25006';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER skills_mutation_policy
BEFORE UPDATE OR DELETE ON skills
FOR EACH ROW EXECUTE FUNCTION enforce_skill_identity_mutation_policy();

CREATE TABLE skill_grants (
    id                 uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id             uuid NOT NULL,
    skill_id           uuid NOT NULL,
    target_project_id  uuid NOT NULL,
    created_at         timestamptz NOT NULL,

    UNIQUE (org_id, skill_id, target_project_id),
    FOREIGN KEY (org_id, skill_id) REFERENCES skills(org_id, id),
    FOREIGN KEY (org_id, target_project_id) REFERENCES projects(org_id, id)
);

-- +goose StatementBegin
CREATE FUNCTION skill_grants_reject_project_self_grant() RETURNS trigger AS $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM skills
        WHERE org_id = NEW.org_id
          AND id = NEW.skill_id
          AND owner_kind = 'project'
          AND owner_project_id = NEW.target_project_id
    ) THEN
        RAISE EXCEPTION 'skill is already available to its owner project'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER skill_grants_no_project_self_grant
    BEFORE INSERT OR UPDATE OF org_id, skill_id, target_project_id ON skill_grants
    FOR EACH ROW
    EXECUTE FUNCTION skill_grants_reject_project_self_grant();

CREATE INDEX skill_grants_target_project_skill_idx
    ON skill_grants(org_id, target_project_id, skill_id);
