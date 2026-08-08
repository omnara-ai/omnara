omnara_run_scoped_startup(){ (
unset omnara_startup_env_file omnara_daemon_env_keys omnara_saved_umask omnara_tmp_dir omnara_env_key
omnara_startup_env_file=${OMNARA_STARTUP_ENV_FILE:?}
omnara_daemon_env_keys=${OMNARA_DAEMON_ENV_KEYS:?}
omnara_saved_umask=$(umask);umask 077
omnara_tmp_dir=$(mktemp -d)||exit 70
[ -n "$omnara_tmp_dir" ]||exit 70
omnara_cleanup_scoped_startup(){
rm -rf "$omnara_tmp_dir"
rm -f "$omnara_startup_env_file"
rmdir "${omnara_startup_env_file%/*}" 2>/dev/null||:
}
trap 'omnara_cleanup_scoped_startup' 0
trap 'omnara_cleanup_scoped_startup;trap - 0 HUP INT TERM;exit 1' HUP INT TERM
printf %s "${OMNARA_STARTUP_SCRIPT_PAYLOAD:?}"|base64 -d >"$omnara_tmp_dir/s"||exit 70
exec 3<"$omnara_startup_env_file" 4<"$omnara_tmp_dir/s"
omnara_cleanup_scoped_startup
trap - 0 HUP INT TERM
unset OMNARA_STARTUP_ENV_FILE OMNARA_DAEMON_ENV_KEYS
for omnara_env_key in $omnara_daemon_env_keys;do unset "$omnara_env_key";done
umask "$omnara_saved_umask"
unset omnara_startup_env_file omnara_daemon_env_keys omnara_saved_umask omnara_tmp_dir omnara_env_key
. /dev/fd/3
exec 3<&-
# Keep startup bytes out of argv; this makes the script fd its $0.
exec /bin/sh /dev/fd/4
);}
if [ -n "${OMNARA_STARTUP_SCRIPT_PAYLOAD:-}" ];then
r startup_script omnara_run_scoped_startup
fi
omnara_cleanup_startup_env
trap - 0 HUP INT TERM
unset OMNARA_STARTUP_SCRIPT_PAYLOAD OMNARA_STARTUP_ENV_FILE OMNARA_DAEMON_ENV_KEYS
