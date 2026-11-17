-- name: GetModelPriceCatalog :one
SELECT version, data FROM model_price_catalog WHERE singleton_key = 1;

-- name: GetChangedModelPriceCatalog :one
SELECT data FROM model_price_catalog WHERE singleton_key = 1 AND version <> sqlc.arg(version);

-- name: SaveModelPriceCatalog :execrows
INSERT INTO model_price_catalog (data, version)
VALUES (sqlc.arg(data), sqlc.arg(version))
ON CONFLICT (singleton_key) DO UPDATE
SET data = EXCLUDED.data, version = EXCLUDED.version
WHERE model_price_catalog.version = sqlc.arg(expected_version);
