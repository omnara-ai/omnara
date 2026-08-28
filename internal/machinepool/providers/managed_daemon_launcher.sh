m=$(umask)
umask 077
h=${HOME:-}
d=$(mktemp -d)
trap 'rm -rf "$d"' 0
trap 'exit 1' HUP INT TERM
r daemon_install curl -qfsSL --connect-timeout 10 -m 60 --max-redirs 5 --max-filesize 1048576 \
  --proto '=http,https' --proto-redir '=https' -o "$d/i" "${OMNARA_API_URL%/api/v1}/install/omnarad.sh"
OMNARA_DAEMON_SEED_PATH="${OMNARA_DAEMON_SEED_PATH:-$b}" r daemon_install /bin/sh "$d/i" --install-only
h=${OMNARA_HOME:-"${h%/}/$n"}
rm -rf "$d"
trap - 0 HUP INT TERM
export PATH="$h/bin${PATH:+:$PATH}"
umask "$m"
exec "$h/bin/omnarad" start --no-service
