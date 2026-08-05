-- name: GetMachineIdentity :one
SELECT installation_id, machine_id
FROM machine_identity
WHERE singleton = 1;

-- name: BindMachineIdentity :exec
INSERT INTO machine_identity(singleton, installation_id, machine_id)
VALUES(1, sqlc.arg(installation_id), sqlc.arg(machine_id));
