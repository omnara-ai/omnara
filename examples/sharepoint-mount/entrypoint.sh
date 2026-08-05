#!/bin/sh
# Container entrypoint: mounts the SharePoint document library with rclone,
# then runs the Omnara machine daemon in the foreground.
set -eu

: "${OMNARA_API_URL:?OMNARA_API_URL is required}"
: "${OMNARA_MACHINE_TOKEN:?OMNARA_MACHINE_TOKEN is required}"

if [ -n "${SHAREPOINT_DRIVE_ID:-}" ]; then
  if [ -z "${SHAREPOINT_RCLONE_TOKEN:-}" ]; then
    : "${SHAREPOINT_TENANT_ID:?SHAREPOINT_TENANT_ID is required unless SHAREPOINT_RCLONE_TOKEN is set}"
    : "${SHAREPOINT_CLIENT_ID:?SHAREPOINT_CLIENT_ID is required unless SHAREPOINT_RCLONE_TOKEN is set}"
    : "${SHAREPOINT_CLIENT_SECRET:?SHAREPOINT_CLIENT_SECRET is required unless SHAREPOINT_RCLONE_TOKEN is set}"
  fi

  mount_path="${SHAREPOINT_MOUNT_PATH:-/mnt/sharepoint}"
  cache_dir=/var/cache/rclone-sharepoint
  config_dir=/run/omnara-sharepoint
  config_file="${config_dir}/rclone.conf"

  if [ ! -e /dev/fuse ]; then
    echo "/dev/fuse is not available; run the container with --device /dev/fuse --cap-add SYS_ADMIN" >&2
    exit 1
  fi

  mkdir -p "$mount_path" "$cache_dir" "$config_dir"
  chmod 0700 "$config_dir"

  if [ -n "${SHAREPOINT_RCLONE_TOKEN:-}" ]; then
    # Delegated auth: a user token from `rclone authorize "onedrive"`.
    cat > "$config_file" << EOF
[sharepoint]
type = onedrive
token = ${SHAREPOINT_RCLONE_TOKEN}
drive_id = ${SHAREPOINT_DRIVE_ID}
drive_type = documentLibrary
EOF
  else
    # Application auth: Entra app client credentials.
    cat > "$config_file" << EOF
[sharepoint]
type = onedrive
client_id = ${SHAREPOINT_CLIENT_ID}
client_secret = ${SHAREPOINT_CLIENT_SECRET}
tenant = ${SHAREPOINT_TENANT_ID}
client_credentials = true
drive_id = ${SHAREPOINT_DRIVE_ID}
drive_type = documentLibrary
disable_site_permission = true
EOF
  fi
  chmod 0600 "$config_file"

  # The credentials live in rclone.conf now; keep them out of the daemon's env.
  unset SHAREPOINT_CLIENT_SECRET SHAREPOINT_RCLONE_TOKEN

  rclone mount sharepoint: "$mount_path" \
    --config "$config_file" \
    --vfs-cache-mode full \
    --cache-dir "$cache_dir" \
    --dir-cache-time 5m \
    --daemon

  for _ in 1 2 3 4 5; do
    if mountpoint -q "$mount_path"; then
      echo "SharePoint mounted at ${mount_path}" >&2
      break
    fi
    sleep 1
  done
  if ! mountpoint -q "$mount_path"; then
    echo "SharePoint mount failed: ${mount_path} is not a mountpoint" >&2
    exit 1
  fi
else
  echo "SHAREPOINT_DRIVE_ID not set; skipping SharePoint mount" >&2
fi

exec /usr/local/bin/omnarad start --no-service
