-- Runtime configuration writers serialize per app after org/project gates and
-- before installation identity/lifecycle/row locks or the app SHARE lock. This
-- prevents two guild setups from creating different first shard sets without
-- upgrading an app lock while another writer holds an installation row.
-- name: LockIntegrationRuntimeConfiguration :exec
SELECT pg_advisory_xact_lock(hashtextextended(
  'integration_runtime_configuration:' || sqlc.arg(integration_app_id)::uuid::text, 0
));

-- The fixed key namespace is also inspected so a wrong-kind conflicting unit
-- fails setup rather than being silently reused or partially replaced.
-- name: ListDiscordRuntimeSetupUnits :many
SELECT unit_key, runtime_kind, spec_revision, configuration
FROM integration_runtime_units
WHERE org_id = sqlc.arg(org_id) AND integration_app_id = sqlc.arg(integration_app_id)
  AND deleted_at IS NULL
  AND (runtime_kind = 'discord_gateway' OR starts_with(unit_key, 'discord_gateway:'))
ORDER BY unit_key;
