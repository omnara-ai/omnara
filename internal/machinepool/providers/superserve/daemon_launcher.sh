set -u
# Superserve guests mount a bare devtmpfs without the /dev/fd and standard
# stream links that scripts (including the daemon bootstrap) expect.
[ -e /dev/fd ] || ln -s /proc/self/fd /dev/fd
[ -e /dev/stdin ] || ln -s /proc/self/fd/0 /dev/stdin
[ -e /dev/stdout ] || ln -s /proc/self/fd/1 /dev/stdout
[ -e /dev/stderr ] || ln -s /proc/self/fd/2 /dev/stderr
umask 077
d=${HOME:-/tmp}/.omnara-superserve
mkdir -p "$d" || exit 1
if [ -f "$d/pid" ]; then
  p=$(cat "$d/pid" 2>/dev/null)
  if [ -n "$p" ] && kill -0 "$p" 2>/dev/null &&
    tr '\0' ' ' <"/proc/$p/cmdline" 2>/dev/null | grep -q omnarad; then
    echo "omnara daemon is already running (pid $p)"
    exit 0
  fi
fi
: >"$d/log"
# setsid gives the daemon its own session when the image has it; otherwise
# ignoring SIGHUP with detached stdio is enough to outlive this exec session.
detach=
if command -v setsid >/dev/null 2>&1; then detach=setsid; fi
$detach /bin/sh -c 'trap "" HUP; echo $$ >"$1/pid" && exec /bin/sh -c "$2"' omnara-daemon "$d" "$1" \
  </dev/null >>"$d/log" 2>&1 &
sleep 2
p=$(cat "$d/pid" 2>/dev/null)
if [ -n "$p" ] && kill -0 "$p" 2>/dev/null; then
  echo "omnara daemon started (pid $p)"
  exit 0
fi
echo "omnara daemon exited early" >&2
cat "$d/log" >&2
exit 1
