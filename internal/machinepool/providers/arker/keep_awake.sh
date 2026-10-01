printf =1 >"$omnara_awake_file"
if [ "$omnara_mode" = boot ]; then
  rm -f "$omnara_daemon_pid_file"
  setsid nohup /bin/sh -c "echo \$\$ >$omnara_daemon_pid_file;$omnara_daemon_launcher" >/tmp/omnarad.log 2>&1 &
else
  curl -fsS -m 2 -o /dev/null "http://127.0.0.1:$omnara_wake_port/" || :
fi
while { [ ! -s "$omnara_daemon_pid_file" ] || kill -0 "$(cat "$omnara_daemon_pid_file")"; } 2>/dev/null; do
  if [ "$(cat "$omnara_awake_file")" = =0 ]; then
    sleep 1
    [ "$(cat "$omnara_awake_file")" = =0 ] && break
  fi
  sleep 1
done
