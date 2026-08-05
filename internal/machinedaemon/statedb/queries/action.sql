-- name: InsertProcessAction :exec
INSERT INTO process_actions(
    action_id,
    process_id,
    action_kind,
    seq,
    effect_committed
)
VALUES(
    sqlc.arg(action_id),
    sqlc.arg(process_id),
    sqlc.arg(action_kind),
    sqlc.arg(seq),
    sqlc.arg(effect_committed)
);

-- name: GetProcessAction :one
SELECT action_id,
       process_id,
       action_kind,
       seq,
       effect_committed
FROM process_actions
WHERE action_id = sqlc.arg(action_id);

-- name: ListProcessActions :many
SELECT action_id,
       process_id,
       action_kind,
       seq,
       effect_committed
FROM process_actions
WHERE process_id = sqlc.arg(process_id)
ORDER BY seq;

-- name: GetProcessActionIDBySequence :one
SELECT action_id
FROM process_actions
WHERE process_id = sqlc.arg(process_id)
  AND seq = sqlc.arg(seq);

-- name: CountProcessActionsBetween :one
SELECT count(*)
FROM process_actions
WHERE process_id = sqlc.arg(process_id)
  AND seq > sqlc.arg(after_seq)
  AND seq < sqlc.arg(before_seq);

-- name: DeleteProcessAction :exec
DELETE FROM process_actions
WHERE action_id = sqlc.arg(action_id)
  AND process_id = sqlc.arg(process_id);
