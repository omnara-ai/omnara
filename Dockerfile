FROM --platform=$BUILDPLATFORM node:24-bookworm-slim@sha256:3638d9a6fe4030bd716be989438248074489337ba3275657f93595428be4fc03 AS web-build
WORKDIR /src/frontend
COPY frontend/package.json frontend/pnpm-lock.yaml frontend/pnpm-workspace.yaml ./
COPY frontend/apps/web/package.json apps/web/package.json
COPY frontend/packages/react/package.json packages/react/package.json
COPY frontend/packages/sdk/package.json packages/sdk/package.json
RUN corepack enable && pnpm install --frozen-lockfile
COPY frontend ./
COPY api/openapi/openapi.yaml /src/api/openapi/openapi.yaml
COPY internal/agentconfig/generated/agent_config.schema.json /src/internal/agentconfig/generated/agent_config.schema.json
RUN pnpm run generate:api && pnpm run typecheck && pnpm run build \
    && rm apps/web/dist/.gitkeep

FROM nginxinc/nginx-unprivileged:1.29.5-alpine@sha256:42a7d7f2ee23e9f5a1dcdf3647ba5c585bbd18f79e79cd817e70e8cd61c55779 AS web
COPY --chown=101:101 frontend/apps/web/nginx.conf /etc/nginx/conf.d/default.conf
COPY --chown=101:101 --from=web-build /src/frontend/apps/web/dist /usr/share/nginx/omnara

FROM --platform=$BUILDPLATFORM golang:1.26.6-bookworm@sha256:116d58cbd88c1297624acc6e967a060012422bacf9930927e23fb719189c6f36 AS go-base
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

FROM go-base AS api-build
ARG TARGETOS
ARG TARGETARCH
RUN mkdir -p frontend/apps/web/dist \
    && : > frontend/apps/web/dist/.gitkeep \
    && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /out/omnara-api ./cmd/api

FROM go-base AS worker-build
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /out/omnara-worker ./cmd/worker

FROM go-base AS maintenance-build
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /out/omnara-maintenance ./cmd/maintenance

FROM --platform=$BUILDPLATFORM golang:1.26.6-bookworm@sha256:116d58cbd88c1297624acc6e967a060012422bacf9930927e23fb719189c6f36 AS mcp-registry-snapshot
WORKDIR /src
COPY go.mod go.sum ./
COPY internal/mcpregistry internal/mcpregistry
COPY tools/mcp-registry-sync tools/mcp-registry-sync
RUN go run ./tools/mcp-registry-sync -out /out/mcp-registry.json

FROM go-base AS migrations-build
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /out/omnara-migrate ./cmd/migrate

FROM gcr.io/distroless/static-debian12:nonroot@sha256:f5b485ea962d9bd1186b2f6b3a061191539b905b82ec395de78cbfae51f20e35 AS runtime
WORKDIR /app

FROM runtime AS api
ENV OMNARA_WEB_SERVING=disabled
ENV OMNARA_MCP_REGISTRY_SNAPSHOT_PATH=/app/mcp-registry/mcp-registry.json
COPY --from=api-build /out/omnara-api /usr/local/bin/omnara-api
COPY --from=mcp-registry-snapshot --chown=nonroot:nonroot /out/mcp-registry.json /app/mcp-registry/mcp-registry.json
ENTRYPOINT ["/usr/local/bin/omnara-api"]

FROM runtime AS worker
COPY --from=worker-build /out/omnara-worker /usr/local/bin/omnara-worker
ENTRYPOINT ["/usr/local/bin/omnara-worker"]

FROM runtime AS maintenance
COPY --from=maintenance-build /out/omnara-maintenance /usr/local/bin/omnara-maintenance
ENTRYPOINT ["/usr/local/bin/omnara-maintenance"]

FROM runtime AS migrations
ENV OMNARA_MIGRATIONS_DIR=/app/migrations
COPY --from=migrations-build /out/omnara-migrate /usr/local/bin/omnara-migrate
COPY --from=go-base /src/migrations /app/migrations
ENTRYPOINT ["/usr/local/bin/omnara-migrate"]
