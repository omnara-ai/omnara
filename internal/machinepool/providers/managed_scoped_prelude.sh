omnara_cleanup_startup_env(){
if [ -n "${OMNARA_STARTUP_ENV_FILE:-}" ];then
rm -f "$OMNARA_STARTUP_ENV_FILE"
rmdir "${OMNARA_STARTUP_ENV_FILE%/*}" 2>/dev/null||:
fi
}
trap 'omnara_cleanup_startup_env' 0
trap 'omnara_cleanup_startup_env;trap - 0 HUP INT TERM;exit 1' HUP INT TERM
