set -u
unset OMNARA_BOOTSTRAP_SCRIPT

startup_script_payload=${OMNARA_STARTUP_SCRIPT_PAYLOAD:?}
unset OMNARA_STARTUP_SCRIPT_PAYLOAD
startup_script=$(printf '%s' "$startup_script_payload" | base64 -d) || {
  echo "omnara startup script decode failed" >&2
  exit 1
}
echo "omnara startup script started" >&2
/bin/sh -c "$startup_script" || {
  startup_status=$?
  echo "omnara startup script failed with exit status ${startup_status}" >&2
  exit "$startup_status"
}
