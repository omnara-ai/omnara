# Runs the Omnara machine daemon with a SharePoint document library mounted at
# /mnt/sharepoint via rclone. Build with the repo root as context; see
# connect-machine.sh.
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 go build -o /omnarad ./cmd/daemon

FROM debian:bookworm-slim
# pandoc and the python3 document libraries give agents tools to read the
# .docx/.xlsx files typically found in SharePoint libraries.
RUN apt-get update \
  && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    fuse3 \
    jq \
    pandoc \
    procps \
    python3 \
    python3-docx \
    python3-openpyxl \
    ripgrep \
    unzip \
  && rm -rf /var/lib/apt/lists/*
# Debian's packaged rclone (1.60) predates OneDrive client-credentials auth
# (rclone >= 1.68), so install the current upstream release.
RUN arch=$(dpkg --print-architecture) \
  && curl -fsSL "https://downloads.rclone.org/rclone-current-linux-${arch}.zip" -o /tmp/rclone.zip \
  && unzip -j /tmp/rclone.zip '*/rclone' -d /usr/local/bin \
  && chmod 0755 /usr/local/bin/rclone \
  && rm /tmp/rclone.zip
RUN mkdir -p /mnt/sharepoint /var/cache/rclone-sharepoint /var/lib/omnara /workspace
COPY --from=build /omnarad /usr/local/bin/omnarad
COPY examples/sharepoint-mount/entrypoint.sh /usr/local/bin/omnara-sharepoint-entrypoint
RUN chmod 0755 /usr/local/bin/omnara-sharepoint-entrypoint
ENV OMNARA_HOME=/var/lib/omnara
WORKDIR /workspace
ENTRYPOINT ["/usr/local/bin/omnara-sharepoint-entrypoint"]
