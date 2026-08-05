-- name: InsertSkill :one
INSERT INTO skills(
    id, org_id, owner_kind, owner_project_id, owner_user_id, name, created_at
) VALUES (
    sqlc.arg(id), sqlc.arg(org_id), sqlc.arg(owner_kind),
    sqlc.narg(owner_project_id), sqlc.narg(owner_user_id),
    sqlc.arg(name), statement_timestamp()
)
RETURNING id;

-- name: GetSkillIDByName :one
SELECT id
FROM skills
WHERE org_id = sqlc.arg(org_id)
  AND owner_kind = sqlc.arg(owner_kind)
  AND name = sqlc.arg(name)
  AND owner_project_id IS NOT DISTINCT FROM sqlc.narg(owner_project_id)
  AND owner_user_id IS NOT DISTINCT FROM sqlc.narg(owner_user_id)
  AND deleted_at IS NULL;

-- name: LockSkill :one
SELECT id
FROM skills
WHERE org_id = sqlc.arg(org_id)
  AND id = sqlc.arg(id)
  AND deleted_at IS NULL
FOR UPDATE;

-- name: NextSkillRevision :one
-- @sqlc-vet-disable skill-revisions-deleted-at
-- Counts soft-deleted revisions too: revision numbers are never reused, and
-- UNIQUE (skill_id, revision) spans deleted rows.
SELECT (COALESCE(MAX(revision), 0) + 1)::int AS revision
FROM skill_revisions
WHERE skill_id = sqlc.arg(skill_id);

-- name: InsertSkillRevision :one
INSERT INTO skill_revisions(
    id, skill_id, revision, description, skill_md, archive_digest,
    created_at
) VALUES (
    sqlc.arg(id), sqlc.arg(skill_id), sqlc.arg(revision), sqlc.arg(description),
    sqlc.arg(skill_md), sqlc.arg(archive_digest),
    statement_timestamp()
)
RETURNING revision;

-- name: ListSkillRevisionIDs :many
-- @sqlc-vet-disable skill-revisions-deleted-at
-- Archive purge enumeration: retries must see revisions soft deleted by a prior attempt.
SELECT id
FROM skill_revisions
WHERE skill_id = sqlc.arg(skill_id)
ORDER BY revision;

-- name: GetSkillByID :one
SELECT s.id, s.org_id, s.owner_kind, s.owner_project_id, s.owner_user_id, s.name,
       s.created_at,
       r.id AS revision_id, r.revision, r.description, r.skill_md,
       r.archive_digest, r.created_at AS updated_at
FROM skills s
JOIN LATERAL (
    SELECT id, revision, description, skill_md, archive_digest, created_at
    FROM skill_revisions
    WHERE skill_id = s.id AND deleted_at IS NULL
    ORDER BY revision DESC
    LIMIT 1
) r ON true
WHERE s.id = sqlc.arg(id) AND s.deleted_at IS NULL;

-- name: GetSkillForOrg :one
SELECT s.id, s.org_id, s.owner_kind, s.owner_project_id, s.owner_user_id, s.name,
       s.created_at,
       r.id AS revision_id, r.revision, r.description, r.skill_md,
       r.archive_digest, r.created_at AS updated_at
FROM skills s
JOIN LATERAL (
    SELECT id, revision, description, skill_md, archive_digest, created_at
    FROM skill_revisions
    WHERE skill_id = s.id AND deleted_at IS NULL
    ORDER BY revision DESC
    LIMIT 1
) r ON true
WHERE s.org_id = sqlc.arg(org_id)
  AND s.id = sqlc.arg(id)
  AND s.deleted_at IS NULL;

-- name: GetSkillForDispatch :one
SELECT s.id, s.org_id, s.owner_kind, s.owner_project_id, s.owner_user_id, s.name,
       s.created_at,
       r.id AS revision_id, r.revision, r.description, r.skill_md,
       r.archive_digest, r.created_at AS updated_at
FROM skills s
JOIN projects p ON p.org_id = s.org_id
JOIN LATERAL (
    SELECT id, revision, description, skill_md, archive_digest, created_at
    FROM skill_revisions
    WHERE skill_id = s.id AND deleted_at IS NULL
    ORDER BY revision DESC
    LIMIT 1
) r ON true
WHERE p.id = sqlc.arg(project_id)
  AND s.id = sqlc.arg(id)
  AND s.deleted_at IS NULL
  AND (
    (s.owner_kind = 'project' AND s.owner_project_id = p.id)
    OR EXISTS (
      SELECT 1
      FROM skill_grants sg
      WHERE sg.org_id = s.org_id
        AND sg.skill_id = s.id
        AND sg.target_project_id = p.id
    )
  );

-- name: ListSkillsByIDs :many
SELECT s.id, s.org_id, s.owner_kind, s.owner_project_id, s.owner_user_id, s.name,
       s.created_at,
       r.id AS revision_id, r.revision, r.description, r.skill_md,
       r.archive_digest, r.created_at AS updated_at
FROM skills s
JOIN LATERAL (
    SELECT id, revision, description, skill_md, archive_digest, created_at
    FROM skill_revisions
    WHERE skill_id = s.id AND deleted_at IS NULL
    ORDER BY revision DESC
    LIMIT 1
) r ON true
WHERE s.org_id = sqlc.arg(org_id)
  AND s.deleted_at IS NULL
  AND s.id = ANY(sqlc.arg(ids)::uuid[])
ORDER BY s.name;

-- name: InsertSkillGrant :one
INSERT INTO skill_grants(id, org_id, skill_id, target_project_id, created_at)
SELECT sqlc.arg(id), sqlc.arg(org_id), sqlc.arg(skill_id), sqlc.arg(target_project_id),
       statement_timestamp()
WHERE EXISTS (
  SELECT 1 FROM skills WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(skill_id) AND deleted_at IS NULL
)
  AND EXISTS (
    SELECT 1 FROM projects WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(target_project_id) AND deleted_at IS NULL
  )
RETURNING id, org_id, skill_id, target_project_id, created_at;

-- name: GetSkillGrantForSourceSkill :one
SELECT id, org_id, skill_id, target_project_id, created_at
FROM skill_grants
WHERE org_id = sqlc.arg(org_id) AND skill_id = sqlc.arg(skill_id) AND id = sqlc.arg(id);

-- name: ListSkillGrantsBySkill :many
WITH listed AS (
  SELECT grant_row.id, grant_row.org_id, grant_row.skill_id,
         grant_row.target_project_id,
         grant_row.created_at, project.name AS target_project_name,
         project.created_at AS target_project_created_at,
         project.updated_at AS target_project_updated_at,
         CASE sqlc.arg(sort_field)::text
           WHEN 'name' THEN lower(project.name)
           WHEN 'created_at' THEN to_char(grant_row.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US')
         END::text AS sort_key,
         false AS sort_is_null
  FROM skill_grants grant_row
  JOIN projects project ON project.org_id = grant_row.org_id
    AND project.id = grant_row.target_project_id
    AND project.deleted_at IS NULL
  WHERE grant_row.org_id = sqlc.arg(org_id)
    AND grant_row.skill_id = sqlc.arg(skill_id)
    AND (sqlc.arg(name_pattern)::text = '' OR project.name ILIKE sqlc.arg(name_pattern)::text ESCAPE '\')
    AND (sqlc.narg(target_project_id)::uuid IS NULL OR project.id = sqlc.narg(target_project_id)::uuid)
)
SELECT id, org_id, skill_id, target_project_id, created_at,
       target_project_name, target_project_created_at, target_project_updated_at,
       sort_key, sort_is_null
FROM listed
WHERE sqlc.arg(cursor_set)::boolean = false
   OR (sqlc.arg(sort_desc)::boolean = false AND (sort_key, id) > (sqlc.arg(cursor_key)::text, sqlc.arg(cursor_id)::uuid))
   OR (sqlc.arg(sort_desc)::boolean = true AND (sort_key, id) < (sqlc.arg(cursor_key)::text, sqlc.arg(cursor_id)::uuid))
ORDER BY CASE WHEN sqlc.arg(sort_desc)::boolean = false THEN sort_key END ASC,
         CASE WHEN sqlc.arg(sort_desc)::boolean = true THEN sort_key END DESC,
         CASE WHEN sqlc.arg(sort_desc)::boolean = false THEN id END ASC,
         CASE WHEN sqlc.arg(sort_desc)::boolean = true THEN id END DESC
LIMIT sqlc.arg(row_limit)::bigint;

-- name: DeleteSkillGrant :one
DELETE FROM skill_grants
WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id)
RETURNING id, org_id, skill_id, target_project_id, created_at;

-- name: SkillAvailableToProject :one
SELECT EXISTS (
  SELECT 1
  FROM skills s
  WHERE s.org_id = sqlc.arg(org_id)
    AND s.id = sqlc.arg(skill_id)
    AND s.deleted_at IS NULL
    AND (
      (s.owner_kind = 'project' AND s.owner_project_id = sqlc.arg(project_id)::uuid)
      OR EXISTS (
        SELECT 1
        FROM skill_grants sg
        WHERE sg.org_id = s.org_id
          AND sg.skill_id = s.id
          AND sg.target_project_id = sqlc.arg(project_id)::uuid
      )
    )
)::boolean AS available;

-- name: ListVisibleOwnedSkills :many
WITH listed AS (
SELECT s.id, s.org_id, s.owner_kind, s.owner_project_id, s.owner_user_id, s.name,
       s.created_at,
       r.id AS revision_id, r.revision, r.description, r.skill_md,
       r.archive_digest, r.created_at AS updated_at,
       CASE sqlc.arg(sort_field)::text
         WHEN 'name' THEN lower(s.name)
         WHEN 'created_at' THEN to_char(s.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US')
         WHEN 'updated_at' THEN to_char(r.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US')
         WHEN 'owner_kind' THEN s.owner_kind
       END::text AS sort_key,
       false AS sort_is_null
FROM skills s
JOIN LATERAL (
    SELECT id, revision, description, skill_md, archive_digest, created_at
    FROM skill_revisions
    WHERE skill_id = s.id AND deleted_at IS NULL
    ORDER BY revision DESC
    LIMIT 1
) r ON true
WHERE s.org_id = sqlc.arg(org_id)
  AND s.deleted_at IS NULL
  AND (sqlc.arg(owner_kind)::text = '' OR s.owner_kind = sqlc.arg(owner_kind)::text)
  AND (sqlc.narg(owner_project_id)::uuid IS NULL OR s.owner_project_id = sqlc.narg(owner_project_id)::uuid)
  AND (
    (s.owner_kind = 'org' AND EXISTS (
      SELECT 1
      FROM org_memberships om
      WHERE om.org_id = s.org_id
        AND (
          (sqlc.narg(user_id)::uuid IS NOT NULL AND om.user_id = sqlc.narg(user_id)::uuid)
          OR (sqlc.narg(org_api_key_id)::uuid IS NOT NULL AND om.org_api_key_id = sqlc.narg(org_api_key_id)::uuid)
        )
    ))
    OR (s.owner_kind = 'project' AND EXISTS (
      SELECT 1
      FROM principal_project_authorization_roles roles
      WHERE roles.org_id = s.org_id
        AND roles.project_id = s.owner_project_id
        AND (
          (sqlc.narg(user_id)::uuid IS NOT NULL AND roles.user_id = sqlc.narg(user_id)::uuid)
          OR (sqlc.narg(org_api_key_id)::uuid IS NOT NULL AND roles.org_api_key_id = sqlc.narg(org_api_key_id)::uuid)
        )
    ))
    OR (s.owner_kind = 'user' AND s.owner_user_id = sqlc.narg(user_id)::uuid AND EXISTS (
      SELECT 1
      FROM org_memberships om
      WHERE om.org_id = s.org_id
        AND om.user_id = sqlc.narg(user_id)::uuid
    ))
  )
  AND (sqlc.arg(name_pattern)::text = '' OR s.name ILIKE sqlc.arg(name_pattern)::text ESCAPE '\')
)
SELECT id, org_id, owner_kind, owner_project_id, owner_user_id, name,
       created_at, revision_id, revision, description,
       skill_md, archive_digest, updated_at, sort_key, sort_is_null
FROM listed
WHERE sqlc.arg(cursor_set)::boolean = false
   OR (sqlc.arg(sort_desc)::boolean = false AND (sort_key, id) > (sqlc.arg(cursor_key)::text, sqlc.arg(cursor_id)::uuid))
   OR (sqlc.arg(sort_desc)::boolean = true AND (sort_key, id) < (sqlc.arg(cursor_key)::text, sqlc.arg(cursor_id)::uuid))
ORDER BY CASE WHEN sqlc.arg(sort_desc)::boolean = false THEN sort_key END ASC,
         CASE WHEN sqlc.arg(sort_desc)::boolean = true THEN sort_key END DESC,
         CASE WHEN sqlc.arg(sort_desc)::boolean = false THEN id END ASC,
         CASE WHEN sqlc.arg(sort_desc)::boolean = true THEN id END DESC
LIMIT sqlc.arg(row_limit)::bigint;

-- name: ListProjectAvailableSkills :many
WITH available AS (
  SELECT s.id, s.org_id, s.owner_kind, s.owner_project_id, s.owner_user_id, s.name,
         s.created_at, NULL::uuid AS grant_id,
         'direct'::text AS availability_source
  FROM skills s
  WHERE s.org_id = sqlc.arg(org_id)
    AND s.deleted_at IS NULL
    AND s.owner_kind = 'project'
    AND s.owner_project_id = sqlc.arg(project_id)::uuid
  UNION ALL
  SELECT s.id, s.org_id, s.owner_kind, s.owner_project_id, s.owner_user_id, s.name,
         s.created_at, sg.id AS grant_id,
         'grant'::text AS availability_source
  FROM skill_grants sg
  JOIN skills s
    ON s.org_id = sg.org_id
   AND s.id = sg.skill_id
  WHERE sg.org_id = sqlc.arg(org_id)
    AND sg.target_project_id = sqlc.arg(project_id)::uuid
    AND s.deleted_at IS NULL
    AND NOT (s.owner_kind = 'project' AND s.owner_project_id = sqlc.arg(project_id)::uuid)
), listed AS (
SELECT a.id, a.org_id, a.owner_kind, a.owner_project_id, a.owner_user_id, a.name,
       a.created_at,
       r.id AS revision_id, r.revision, r.description, r.skill_md,
       r.archive_digest, r.created_at AS updated_at,
       a.grant_id, a.availability_source,
       CASE sqlc.arg(sort_field)::text
         WHEN 'name' THEN lower(a.name)
         WHEN 'created_at' THEN to_char(a.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US')
         WHEN 'updated_at' THEN to_char(r.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US')
         WHEN 'owner_kind' THEN a.owner_kind
         WHEN 'availability_source' THEN a.availability_source
       END::text AS sort_key,
       false AS sort_is_null
FROM available a
JOIN LATERAL (
    SELECT id, revision, description, skill_md, archive_digest, created_at
    FROM skill_revisions
    WHERE skill_id = a.id
      AND deleted_at IS NULL
    ORDER BY revision DESC
    LIMIT 1
) r ON true
WHERE (sqlc.arg(name_pattern)::text = '' OR a.name ILIKE sqlc.arg(name_pattern)::text ESCAPE '\')
  AND (COALESCE(cardinality(sqlc.arg(owner_kinds)::text[]), 0) = 0 OR a.owner_kind = ANY(sqlc.arg(owner_kinds)::text[]))
  AND (COALESCE(cardinality(sqlc.arg(availability_sources)::text[]), 0) = 0 OR a.availability_source = ANY(sqlc.arg(availability_sources)::text[]))
)
SELECT id, org_id, owner_kind, owner_project_id, owner_user_id, name,
       created_at, revision_id, revision, description,
       skill_md, archive_digest, updated_at, grant_id, availability_source,
       sort_key, sort_is_null
FROM listed
WHERE sqlc.arg(cursor_set)::boolean = false
  OR (sqlc.arg(sort_desc)::boolean = false AND (sort_key, id) > (sqlc.arg(cursor_key)::text, sqlc.arg(cursor_id)::uuid))
  OR (sqlc.arg(sort_desc)::boolean = true AND (sort_key, id) < (sqlc.arg(cursor_key)::text, sqlc.arg(cursor_id)::uuid))
ORDER BY CASE WHEN sqlc.arg(sort_desc)::boolean = false THEN sort_key END ASC,
         CASE WHEN sqlc.arg(sort_desc)::boolean = true THEN sort_key END DESC,
         CASE WHEN sqlc.arg(sort_desc)::boolean = false THEN id END ASC,
         CASE WHEN sqlc.arg(sort_desc)::boolean = true THEN id END DESC
LIMIT sqlc.arg(row_limit)::bigint;

-- Compiled agent configs reference skills by public id, so the caller passes
-- the encoded skill id rather than the raw uuid.
-- name: SkillHasActiveAgentReferences :one
SELECT EXISTS (
  SELECT 1
  FROM agents agent
  JOIN agent_configs config
    ON config.project_id = agent.project_id
   AND config.id = agent.current_config_id
  WHERE agent.org_id = sqlc.arg(org_id)
    AND agent.state = 'active'
    AND config.compiled_definition->'skills' @> jsonb_build_array(jsonb_build_object('public_id', sqlc.arg(skill_public_id)::text))
) AS has_active_agent_references;

-- name: DeleteSkill :execrows
UPDATE skills
SET deleted_at = statement_timestamp()
WHERE org_id = sqlc.arg(org_id)
  AND id = sqlc.arg(id)
  AND deleted_at IS NULL;

-- name: DeleteSkillRevisions :exec
UPDATE skill_revisions SET deleted_at = statement_timestamp()
WHERE skill_id = sqlc.arg(skill_id) AND deleted_at IS NULL;

-- name: DeleteSkillGrants :exec
DELETE FROM skill_grants
WHERE org_id = sqlc.arg(org_id) AND skill_id = sqlc.arg(skill_id);
