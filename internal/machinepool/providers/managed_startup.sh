if [ -n "${OMNARA_STARTUP_SCRIPT_PAYLOAD:-}" ];then
s=$(printf %s "$OMNARA_STARTUP_SCRIPT_PAYLOAD"|base64 -d)
unset OMNARA_STARTUP_SCRIPT_PAYLOAD
r startup_script /bin/sh -c "$s"
fi
