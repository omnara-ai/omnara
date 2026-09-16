package tenki

import (
	"fmt"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

func bootstrapScript(env map[string]string) (string, error) {
	environment, err := environmentScript(env)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`set -eu
umask 077
d=/home/tenki/.omnara-provider
mkdir -p "$d"
exec 9>"$d/bootstrap.lock"
flock 9
if [ ! -f "$d/environment" ]; then
  printf '%%s' %s >"$d/environment.tmp"
  mv "$d/environment.tmp" "$d/environment"
fi
printf '%%s' %s >"$d/boot.tmp"
mv "$d/boot.tmp" "$d/boot"
(
  flock -n 8 || exit 0
  . "$d/environment"
  exec /bin/sh "$d/boot"
) 8>"$d/daemon.lock" 9>&- </dev/null >>"$d/daemon.log" 2>&1 &
`, shellQuote(environment), shellQuote(providers.ManagedBootScript())), nil
}
