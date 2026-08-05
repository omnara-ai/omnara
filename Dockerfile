FROM --platform=$BUILDPLATFORM node:22-bookworm-slim@sha256:6c74791e557ce11fc957704f6d4fe134a7bc8d6f5ca4403205b2966bd488f6b3 AS web-build
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

FROM --platform=$BUILDPLATFORM golang:1.26.5-bookworm@sha256:1ecb7edf62a0408027bd5729dfd6b1b8766e578e8df93995b225dfd0944eb651 AS go-base
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

FROM go-base AS migrations-build
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /out/omnara-migrate ./cmd/migrate

FROM gcr.io/distroless/static-debian12:nonroot@sha256:f5b485ea962d9bd1186b2f6b3a061191539b905b82ec395de78cbfae51f20e35 AS runtime
WORKDIR /app

FROM runtime AS api
ENV OMNARA_WEB_SERVING=disabled
COPY --from=api-build /out/omnara-api /usr/local/bin/omnara-api
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
