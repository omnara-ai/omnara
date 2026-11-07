-- name: ReserveProjectMachineGrantCreation :one
-- ON CONFLICT DO NOTHING does not lock an existing receipt while the caller
-- acquires its lifecycle lock. Revocation takes that lock before deleting a
-- grant and setting its receipt's grant_id to NULL.
INSERT INTO project_machine_grant_creations(org_id, project_id, idempotency_key, request_digest)
VALUES (sqlc.arg(org_id), sqlc.arg(project_id), sqlc.arg(idempotency_key),
        encode(sha256(convert_to(sqlc.arg(request)::jsonb::text, 'UTF8')), 'hex'))
ON CONFLICT (project_id, idempotency_key) DO NOTHING
RETURNING idempotency_key;

-- name: GetProjectMachineGrantCreation :one
SELECT grant_id,
       request_digest = encode(sha256(convert_to(sqlc.arg(request)::jsonb::text, 'UTF8')), 'hex') AS matches_request
FROM project_machine_grant_creations
WHERE org_id = sqlc.arg(org_id) AND project_id = sqlc.arg(project_id)
  AND idempotency_key = sqlc.arg(idempotency_key);

-- name: CompleteProjectMachineGrantCreation :execrows
UPDATE project_machine_grant_creations
SET grant_id = sqlc.arg(grant_id)::uuid
WHERE org_id = sqlc.arg(org_id) AND project_id = sqlc.arg(project_id)
  AND idempotency_key = sqlc.arg(idempotency_key) AND grant_id IS NULL;

-- name: ReserveProjectMachinePoolGrantCreation :one
-- ON CONFLICT DO NOTHING does not lock an existing receipt while the caller
-- acquires its lifecycle lock. Revocation takes that lock before deleting a
-- grant and setting its receipt's grant_id to NULL.
INSERT INTO project_machine_pool_grant_creations(org_id, project_id, idempotency_key, request_digest)
VALUES (sqlc.arg(org_id), sqlc.arg(project_id), sqlc.arg(idempotency_key),
        encode(sha256(convert_to(sqlc.arg(request)::jsonb::text, 'UTF8')), 'hex'))
ON CONFLICT (project_id, idempotency_key) DO NOTHING
RETURNING idempotency_key;

-- name: GetProjectMachinePoolGrantCreation :one
SELECT grant_id,
       request_digest = encode(sha256(convert_to(sqlc.arg(request)::jsonb::text, 'UTF8')), 'hex') AS matches_request
FROM project_machine_pool_grant_creations
WHERE org_id = sqlc.arg(org_id) AND project_id = sqlc.arg(project_id)
  AND idempotency_key = sqlc.arg(idempotency_key);

-- name: CompleteProjectMachinePoolGrantCreation :execrows
UPDATE project_machine_pool_grant_creations
SET grant_id = sqlc.arg(grant_id)::uuid
WHERE org_id = sqlc.arg(org_id) AND project_id = sqlc.arg(project_id)
  AND idempotency_key = sqlc.arg(idempotency_key) AND grant_id IS NULL;
