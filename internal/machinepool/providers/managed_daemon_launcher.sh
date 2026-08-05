set -eu
original_umask=$(umask)
umask 077
unset OMNARA_BOOTSTRAP_SCRIPT OMNARA_STARTUP_SCRIPT_PAYLOAD

api_url=${OMNARA_API_URL:?OMNARA_API_URL is required}
home=${HOME:-}

tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/omnarad-launcher.XXXXXX")
installer=$tmp_dir/omnarad.sh
trap 'rm -rf "$tmp_dir"' 0
trap 'exit 1' HUP INT TERM

curl --disable --fail --silent --show-error --location \
  --connect-timeout 10 --max-time 60 --max-redirs 5 --max-filesize 1048576 \
  --proto '=http,https' --proto-redir '=https' \
  --output "$installer" "${api_url%/}/install/omnarad.sh"

OMNARA_DAEMON_SEED_PATH="${OMNARA_DAEMON_SEED_PATH:-$omnara_daemon_seed_path}" \
  /bin/sh "$installer" --install-only

daemon_home=${OMNARA_HOME:-"${home%/}/$omnara_daemon_home_dir"}
rm -rf "$tmp_dir"
trap - 0 HUP INT TERM
export PATH="$daemon_home/bin${PATH:+:$PATH}"
umask "$original_umask"
exec "$daemon_home/bin/omnarad" start --no-service
