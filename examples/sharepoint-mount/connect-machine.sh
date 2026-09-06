#!/bin/sh
# Builds and runs a Docker container that mounts a SharePoint document library
# and runs the Omnara machine daemon alongside it. Register the machine first
# in the Omnara web console (Overview > Machines > Connect machine) and paste
# the machine token into .env. See README.md.
#
# Configuration comes from .env next to this script (copy .env.example);
# values in .env override the inherited environment.
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/../.." && pwd)

if [ -f "$script_dir/.env" ]; then
  set -a
  # shellcheck disable=SC1091
  . "$script_dir/.env"
  set +a
fi

: "${OMNARA_MACHINE_TOKEN:?OMNARA_MACHINE_TOKEN is required (from Connect machine in the web console)}"
api_url=${OMNARA_API_URL:-https://api.omnara.com/v1}

if ! command -v docker > /dev/null 2>&1; then
  echo "docker is required: https://docs.docker.com/get-docker/" >&2
  exit 1
fi

case "$api_url" in
*localhost* | *127.0.0.1*)
  echo "OMNARA_API_URL points at localhost, which is unreachable from inside the container." >&2
  echo "For a local API, use http://host.docker.internal:8080/api/v1 instead." >&2
  exit 1
  ;;
esac

image="${OMNARA_DOCKER_IMAGE:-omnara-sharepoint-daemon}"
container="${OMNARA_DOCKER_CONTAINER:-omnara-sharepoint-daemon}"

docker build -f "$script_dir/Dockerfile" -t "$image" "$repo_root"
docker rm -f "$container" > /dev/null 2>&1 || true

echo "starting machine daemon in container $container" >&2
exec docker run --rm --name "$container" \
  --cap-add SYS_ADMIN \
  --device /dev/fuse \
  --add-host host.docker.internal:host-gateway \
  -e OMNARA_API_URL="$api_url" \
  -e OMNARA_MACHINE_TOKEN \
  -e SHAREPOINT_TENANT_ID \
  -e SHAREPOINT_CLIENT_ID \
  -e SHAREPOINT_CLIENT_SECRET \
  -e SHAREPOINT_RCLONE_TOKEN \
  -e SHAREPOINT_DRIVE_ID \
  "$image"
