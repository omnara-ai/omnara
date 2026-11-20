printf =1 >"$omnara_awake_file"
if [ "$omnara_mode" = boot ] &&
  ! { [ -s "$omnara_daemon_pid_file" ] && kill -0 "$(cat "$omnara_daemon_pid_file")"; } 2>/dev/null; then
  rm -f "$omnara_daemon_pid_file"
  setsid /bin/sh -c 'echo $$ >"$1" && exec /bin/sh -c "$2"' omnara-daemon \
    "$omnara_daemon_pid_file" "$omnara_daemon_launcher" </dev/null >>"$omnara_daemon_log" 2>&1 &
else
  curl --disable --noproxy '*' -fsS -m 2 -o /dev/null "http://127.0.0.1:$omnara_wake_port/" || :
fi
while { [ ! -s "$omnara_daemon_pid_file" ] || kill -0 "$(cat "$omnara_daemon_pid_file")"; } 2>/dev/null; do
  if [ "$(cat "$omnara_awake_file" 2>/dev/null)" = =0 ]; then
    sleep 1
    [ "$(cat "$omnara_awake_file" 2>/dev/null)" = =0 ] && break
  fi
  sleep 1
done
