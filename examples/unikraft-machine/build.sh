#!/bin/sh
set -eu

image=${1:?usage: build.sh <organization/image:tag> <omnarad-version>}
version=${2:?usage: build.sh <organization/image:tag> <omnarad-version>}
printf '%s\n' "$version" | grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$' || {
  echo "omnarad version must be a release version such as 0.1.0" >&2
  exit 1
}
example_dir=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
repo_dir=$(CDPATH= cd -- "$example_dir/../.." && pwd)
build_dir=$(mktemp -d)
trap 'rm -rf "$build_dir"' EXIT HUP INT TERM

cp "$example_dir/Kraftfile" "$example_dir/Dockerfile" "$build_dir/"
(
  cd "$repo_dir"
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -buildmode=pie \
    -ldflags "-X github.com/omnara-ai/omnara/internal/omnarad.version=$version" \
    -o "$build_dir/omnarad" ./cmd/daemon
)
(
  cd "$build_dir"
  unikraft build . --output "$image"
)
