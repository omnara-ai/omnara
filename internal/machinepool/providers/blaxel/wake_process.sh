set -u
i=0
while [ "$i" -lt 3 ]; do
  if curl --disable --noproxy '*' --fail --silent --show-error \
    --connect-timeout 1 --max-time 1 "$omnara_wake_listener_url" >/dev/null; then
    exit 0
  fi
  i=$((i+1))
  [ "$i" -ge 3 ] || sleep 1
done
exit 1
