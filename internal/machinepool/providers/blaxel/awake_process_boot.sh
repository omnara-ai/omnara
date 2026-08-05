set -eu
omnara_supervisor_pid=$$
omnara_supervisor_start=$(sed 's/^.*) //' /proc/$omnara_supervisor_pid/stat | cut -d ' ' -f 20)
case "$omnara_supervisor_start" in
  ''|*[!0-9]*)
    echo "omnara could not read the Blaxel daemon process start time" >&2
    exit 1
    ;;
esac
omnara_awake_process_name=$omnara_awake_process_name_prefix$omnara_supervisor_pid
omnara_awake_process_command="supervisor_pid=$omnara_supervisor_pid;supervisor_start=$omnara_supervisor_start;"\
"while [ -r /proc/\$supervisor_pid/stat ] && "\
"[ x\$(sed 's/^.*) //' /proc/\$supervisor_pid/stat 2>/dev/null | cut -d ' ' -f 20) = x\$supervisor_start ]; "\
"do sleep 1; done"
omnara_awake_process_body=$(
  printf '{"name":"%s","command":"%s","keepAlive":true,"timeout":0,"waitForCompletion":false}' \
    "$omnara_awake_process_name" "$omnara_awake_process_command"
)
if ! omnara_awake_process_response=$(
  curl --disable --noproxy '*' --fail --silent --show-error \
    --connect-timeout 1 --max-time 3 --request POST \
    --header 'Content-Type: application/json' --data "$omnara_awake_process_body" \
    "$omnara_blaxel_process_api_url"
); then
  if ! omnara_awake_process_response=$(
    curl --disable --noproxy '*' --fail --silent --show-error \
      --connect-timeout 1 --max-time 3 \
      "$omnara_blaxel_process_api_url/$omnara_awake_process_name"
  ); then
    echo "omnara could not create the Blaxel awake process" >&2
    exit 1
  fi
fi
if ! printf '%s' "$omnara_awake_process_response" | \
  grep -Eq '"status"[[:space:]]*:[[:space:]]*"running"'; then
  echo "omnara Blaxel awake process is not running" >&2
  exit 1
fi
if ! printf '%s' "$omnara_awake_process_response" | \
  grep -Eq '"keepAlive"[[:space:]]*:[[:space:]]*true'; then
  echo "omnara Blaxel awake process is not keeping the sandbox awake" >&2
  exit 1
fi
