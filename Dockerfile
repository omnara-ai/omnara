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

FROM nginx/nginx-unprivileged:1.31.8-alpine-slim@sha256:2f293597c9eea53645c61d70942d32b544a8007aeb586e0237e6ad637323b81b AS web
USER root
RUN apk add --no-cache 'zlib>=1.4-r0' \
    && rm /usr/share/nginx/html/index.html /usr/share/nginx/html/50x.html
USER 101
COPY --chown=101:101 frontend/apps/web/nginx.conf /etc/nginx/conf.d/default.conf
COPY --chown=101:101 --from=web-build /src/frontend/apps/web/dist /usr/share/nginx/omnara
RUN nginx -t

FROM --platform=$BUILDPLATFORM golang:1.27.4-bookworm@sha256:4fa7c968ba93f1635670522dbb01a2c78150443e1385746924566079f36570ea AS go-base
WORKDIR /src
COPY go.mod go.sum ./
COPY observability/go.mod observability/go.sum ./observability/
RUN go mod download && mkdir -p /out/memory
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
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /out/omnara-worker ./cmd/worker \
    && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /out/omnara-sandbox ./cmd/sandbox \
    && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /out/omnara-chroot-sandbox ./cmd/chroot-sandbox

FROM go-base AS maintenance-build
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /out/omnara-maintenance ./cmd/maintenance

FROM --platform=$BUILDPLATFORM golang:1.27.4-bookworm@sha256:4fa7c968ba93f1635670522dbb01a2c78150443e1385746924566079f36570ea AS mcp-registry-snapshot
WORKDIR /src
COPY go.mod go.sum ./
COPY observability/go.mod observability/go.sum ./observability/
COPY internal/mcpregistry internal/mcpregistry
COPY tools/mcp-registry-sync tools/mcp-registry-sync
RUN go run ./tools/mcp-registry-sync -out /out/mcp-registry.json

FROM go-base AS migrations-build
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /out/omnara-migrate ./cmd/migrate

FROM gcr.io/distroless/static-debian13:nonroot@sha256:25ac8f0c3b2334a9eb05452266bd4697864a8c8ef0ef71e5e6480d7f92868329 AS runtime
WORKDIR /app
ADD --chmod=0444 https://truststore.pki.rds.amazonaws.com/global/global-bundle.pem /etc/ssl/rds-global-bundle.pem

FROM runtime AS api
COPY --from=go-base --chown=nonroot:nonroot /out/memory /var/lib/omnara/memory
ENV OMNARA_WEB_SERVING=disabled
ENV OMNARA_MCP_REGISTRY_SNAPSHOT_PATH=/app/mcp-registry/mcp-registry.json
COPY --from=api-build /out/omnara-api /usr/local/bin/omnara-api
COPY --from=mcp-registry-snapshot --chown=nonroot:nonroot /out/mcp-registry.json /app/mcp-registry/mcp-registry.json
ENTRYPOINT ["/usr/local/bin/omnara-api"]

FROM debian:trixie-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2 AS file-tools
RUN --mount=type=cache,target=/var/lib/apt/lists,sharing=locked \
    apt-get update && apt-get install -y --no-install-recommends ripgrep sed libcap2-bin
COPY cmd/chroot-sandbox/install.sh /src/chroot-sandbox/install.sh
COPY --from=worker-build /out/omnara-chroot-sandbox /src/omnara-chroot-sandbox
RUN sh /src/chroot-sandbox/install.sh /src/omnara-chroot-sandbox /out

FROM gcr.io/distroless/cc-debian13:nonroot@sha256:cb87acc574abe719b6e5c547384defa2f7b1f24af9605d3f618cf852495d8be2 AS worker
WORKDIR /app
ADD --chmod=0444 https://truststore.pki.rds.amazonaws.com/global/global-bundle.pem /etc/ssl/rds-global-bundle.pem
COPY --from=file-tools /usr/bin/rg /usr/local/bin/
COPY --from=file-tools /usr/lib/*-linux-gnu/libpcre2-8.so.0 /usr/lib/
COPY --from=go-base --chown=nonroot:nonroot /out/memory /var/lib/omnara/memory
COPY --from=worker-build /out/omnara-worker /out/omnara-sandbox /usr/local/bin/
COPY --from=file-tools /out/ /
ENTRYPOINT ["/usr/local/bin/omnara-worker"]

FROM runtime AS maintenance
COPY --from=go-base --chown=nonroot:nonroot /out/memory /var/lib/omnara/memory
COPY --from=maintenance-build /out/omnara-maintenance /usr/local/bin/omnara-maintenance
ENTRYPOINT ["/usr/local/bin/omnara-maintenance"]

FROM runtime AS migrations
ENV OMNARA_MIGRATIONS_DIR=/app/migrations
COPY --from=migrations-build /out/omnara-migrate /usr/local/bin/omnara-migrate
COPY --from=go-base /src/migrations /app/migrations
ENTRYPOINT ["/usr/local/bin/omnara-migrate"]
