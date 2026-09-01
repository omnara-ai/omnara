---
name: verify-omnara
description: Run Omnara's fast repository gate and focused checks for a local change.
---

# Verify Omnara

Omnara is a Go + Node 24 monorepo (Corepack). The maintainer-facing fast gate is `make verify`.

## Fast gate

From the repository root:

```sh
make verify
```

That is `verify-go` (`verify-static`, `unit`, `race-machinedaemon`) plus `web-check`.

For a Go-only slice, run the packages you touched:

```sh
go test ./internal/httpapi/httpjson ./internal/storage/listing
```

Do not treat `go test ./...` as a substitute for `make verify-static` when you change OpenAPI, SQL, or generated clients.

## Heavier suites

- `make test-integration` — database-backed tests (Docker Compose: Postgres, Redis, MinIO)
- `make test-service-e2e` — deterministic service end-to-end
- `make web-check-all` — frontend gate plus React Doctor

Live provider tests (`make test-live-*`) need credentials; skip them unless the change is in a provider adapter.

## Generated files

Do not edit generated files by hand. After an OpenAPI contract change:

```sh
make openapi-generate
make docs-openapi
make web-generate
```

After SQL query changes:

```sh
make sqlc-generate
```

Commit generated output with the source change.

## Pull request titles

Use a conventional title accepted by `.github/pr-title-config.json`, for example `fix: handle expired sessions` or `test: cover httpjson decode guards`.
