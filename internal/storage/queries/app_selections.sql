-- Read other immutable plans without locking their receipts. The caller holds
-- its own receipt and then the conversation gate; locking another receipt here
-- would invert that order. Identity omits slot to reserve the entire N-slot set.
-- The receipt's app scopes both launches and ordinary follow-ups; independently
-- configured apps never reserve one another's conversation.
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
