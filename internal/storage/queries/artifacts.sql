-- name: InsertArtifact :one
WITH inserted AS (
  INSERT INTO artifacts(id, agent_id, content_type, filename, digest, size_bytes, idempotency_key, created_at)
  SELECT sqlc.arg(id), agent.id,
         sqlc.arg(content_type), sqlc.narg(filename),
         sqlc.narg(digest), sqlc.narg(size_bytes), sqlc.narg(idempotency_key), transaction_timestamp()
  FROM agents agent
  WHERE agent.project_id = sqlc.arg(project_id) AND agent.id = sqlc.arg(agent_id)
  ON CONFLICT (agent_id, idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING
  RETURNING id, agent_id, content_type, filename, digest, size_bytes, idempotency_key, created_at
)
SELECT inserted.id, agent.project_id, inserted.agent_id, inserted.content_type,
       coalesce(inserted.filename, '') AS filename,
       coalesce(inserted.digest, '') AS digest,
       inserted.size_bytes,
       coalesce(inserted.idempotency_key, '') AS idempotency_key,
       inserted.created_at
FROM inserted
JOIN agents agent ON agent.id = inserted.agent_id;

-- name: GetArtifact :one
SELECT artifact.id, agent.project_id, artifact.agent_id, artifact.content_type,
       coalesce(artifact.filename, '') AS filename, coalesce(artifact.digest, '') AS digest,
       artifact.size_bytes, coalesce(artifact.idempotency_key, '') AS idempotency_key,
       artifact.created_at
FROM artifacts artifact
JOIN agents agent ON agent.id = artifact.agent_id
WHERE agent.project_id = sqlc.arg(project_id) AND artifact.agent_id = sqlc.arg(agent_id) AND artifact.id = sqlc.arg(id);

-- name: GetArtifactByIdempotencyKey :one
SELECT artifact.id, agent.project_id, artifact.agent_id, artifact.content_type,
       coalesce(artifact.filename, '') AS filename, coalesce(artifact.digest, '') AS digest,
       artifact.size_bytes, coalesce(artifact.idempotency_key, '') AS idempotency_key,
       artifact.created_at
FROM artifacts artifact
JOIN agents agent ON agent.id = artifact.agent_id
WHERE agent.project_id = sqlc.arg(project_id) AND artifact.agent_id = sqlc.arg(agent_id) AND artifact.idempotency_key = sqlc.arg(idempotency_key)::text;

-- name: ListArtifactsByIDs :many
SELECT artifact.id, agent.project_id, artifact.agent_id, artifact.content_type,
       coalesce(artifact.filename, '') AS filename, coalesce(artifact.digest, '') AS digest,
       artifact.size_bytes, coalesce(artifact.idempotency_key, '') AS idempotency_key,
       artifact.created_at
FROM artifacts artifact
JOIN agents agent ON agent.id = artifact.agent_id
WHERE agent.project_id = sqlc.arg(project_id) AND artifact.agent_id = sqlc.arg(agent_id) AND artifact.id = ANY(sqlc.arg(ids)::uuid[]);
