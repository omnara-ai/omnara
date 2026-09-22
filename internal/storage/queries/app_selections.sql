-- The caller holds its receipt, then the conversation gate. Locking another
-- receipt here would invert that order and deadlock concurrent selections.
-- name: FindInboxSelectionReservations :many
WITH matches AS MATERIALIZED (
  SELECT id, state
  FROM integration_inbox
  WHERE project_id = sqlc.arg(project_id) AND app_id = sqlc.arg(app_id)
    AND id <> sqlc.arg(receipt_id) AND plan IS NOT NULL
    AND state IN ('pending', 'processing')
    AND jsonb_path_query_array(plan, '$.*.selection') @> jsonb_build_array(sqlc.arg(selection)::jsonb)
)
SELECT id, state FROM matches
ORDER BY id
LIMIT 2;
