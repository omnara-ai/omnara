-- Parent IDs come from already scoped trigger reads or the internal due-claim
-- selection; one batch also serves claims spanning several projects.
-- name: ListCronTriggerChannelBindings :many
SELECT project_id, cron_trigger_id, channel_id,
  receive_allowed, read_allowed, send_allowed,
  reply_receive_allowed, reply_read_allowed, reply_send_allowed
FROM cron_trigger_channel_bindings
WHERE cron_trigger_id = ANY(sqlc.arg(trigger_ids)::uuid[])
ORDER BY cron_trigger_id, channel_id;

-- name: DeleteCronTriggerChannelBindings :exec
DELETE FROM cron_trigger_channel_bindings
WHERE project_id = sqlc.arg(project_id) AND cron_trigger_id = sqlc.arg(cron_trigger_id);

-- name: InsertCronTriggerChannelBinding :exec
INSERT INTO cron_trigger_channel_bindings (
  project_id, cron_trigger_id, channel_id,
  receive_allowed, read_allowed, send_allowed,
  reply_receive_allowed, reply_read_allowed, reply_send_allowed
) VALUES (
  sqlc.arg(project_id), sqlc.arg(cron_trigger_id), sqlc.arg(channel_id),
  sqlc.arg(receive_allowed), sqlc.arg(read_allowed), sqlc.arg(send_allowed),
  sqlc.narg(reply_receive_allowed), sqlc.narg(reply_read_allowed), sqlc.narg(reply_send_allowed)
);
