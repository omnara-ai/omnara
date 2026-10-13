# Default image for the terminal-bench pool. Task runs override it per agent
# with an image built from the task's own environment (see src/images.ts).
FROM ubuntu:24.04
RUN apt-get update \
  && apt-get install -y --no-install-recommends ca-certificates curl git \
  && rm -rf /var/lib/apt/lists/*
COPY --from=ghcr.io/blaxel-ai/sandbox:latest /sandbox-api /usr/local/bin/sandbox-api
ENV HOME=/root
ENTRYPOINT ["/usr/local/bin/sandbox-api"]
