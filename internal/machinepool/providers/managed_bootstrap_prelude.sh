set -eu
r(){ (
g=$1;shift
d=$(mktemp -d);trap 'rm -rf "$d"' 0
mkfifo "$d/p"
(tee /dev/stderr <"$d/p"|tail -c 4097 >"$d/t")&c=$!
x=0;"$@" >"$d/p" 2>&1||x=$?
[ "$x" -eq 0 ]&&exit
(sleep 1;kill "$c" 2>/dev/null)&w=$!
z=0;wait "$c"||z=$?
kill "$w" 2>/dev/null||:;wait "$w" 2>/dev/null||:
curl -qfsS --connect-timeout 3 -m 5 --retry 2 \
-H "Authorization: Bearer ${OMNARA_MACHINE_TOKEN:?}" -H Content-Type:text/plain \
--data-binary @"$d/t" -o /dev/null "${OMNARA_API_URL:?}/api/v1/daemon/bootstrap/failures?stage=$g&exit_status=$x&capture_status=$z"||:
exit "$x"
);}
if [ -n "${OMNARA_STARTUP_SCRIPT_PAYLOAD:-}" ];then
s=$(printf %s "$OMNARA_STARTUP_SCRIPT_PAYLOAD"|base64 -d)
unset OMNARA_STARTUP_SCRIPT_PAYLOAD
r startup_script /bin/sh -c "$s"
fi
