set -u
umask 077
b=${HOME:-/tmp}/.omnara-boxd
mkdir -p "$b" || exit 1
if [ -f "$b/pid" ]; then
  p=$(cat "$b/pid" 2>/dev/null)
  if [ -n "$p" ] && kill -0 "$p" 2>/dev/null &&
    tr '\0' ' ' <"/proc/$p/cmdline" 2>/dev/null | grep -q -e omnarad -e "$b/boot"; then
    cat >/dev/null
    echo "omnara daemon bootstrap is already running (pid $p)"
    exit 0
  fi
fi
cat >"$b/boot.tmp" || exit 1
mv -f "$b/boot.tmp" "$b/boot" || exit 1
: >"$b/log"
setsid /bin/sh -c 'echo $$ >"$1/pid" && exec /bin/sh "$1/boot"' omnara-boot "$b" \
  </dev/null >>"$b/log" 2>&1 &
sleep 1
p=$(cat "$b/pid" 2>/dev/null)
if [ -n "$p" ] && kill -0 "$p" 2>/dev/null; then
  # The boot script carries the machine's secrets. The running shell holds
  # the inode, so unlinking it keeps the payload out of snapshots and forks
  # without disturbing the bootstrap.
  rm -f "$b/boot"
  echo "omnara daemon bootstrap started (pid $p)"
  exit 0
fi
echo "omnara daemon bootstrap exited early" >&2
cat "$b/log" >&2
exit 1
