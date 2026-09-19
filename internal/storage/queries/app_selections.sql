-- Read other immutable plans without locking their receipts. The caller holds
-- its own receipt and then the conversation gate; locking another receipt here
-- would invert that order. Identity omits slot to reserve the entire N-slot set.
-- name: FindInboxSelectionReservations :many
WITH matches AS MATERIALIZED (
  SELECT id, state
  FROM integration_inbox
  WHERE project_id = sqlc.arg(project_id) AND connection_id = sqlc.arg(connection_id)
    AND id <> sqlc.arg(receipt_id) AND plan IS NOT NULL
    AND state IN ('pending', 'processing', 'failed')
    AND (sqlc.arg(include_failed)::boolean OR state <> 'failed')
    AND jsonb_path_query_array(plan, '$.*.selection') @> jsonb_build_array(sqlc.arg(selection)::jsonb)
)
SELECT id, state FROM matches
ORDER BY id
LIMIT 2;
