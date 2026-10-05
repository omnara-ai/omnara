package boxd

import (
	_ "embed"
	"maps"
	"regexp"
	"slices"
	"strings"
)

const bootstrapKeepAwakeScript = `(while read -r c </proc/$$/comm && [ "$c" != omnarad ]; do ` +
	`curl -fsSI -m 10 -o /dev/null "$OMNARA_INSTALLER_URL" || :; sleep 20; done) >/dev/null 2>&1 &` + "\n"

//go:embed managed_boot_launcher.sh
var managedBootLauncherScript string

var envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func launcherCommand() string {
	return strings.TrimSpace(managedBootLauncherScript)
}

func bootPayload(env map[string]string, script string) []byte {
	var payload strings.Builder
	payload.WriteString("#!/bin/sh\n")
	for _, key := range slices.Sorted(maps.Keys(env)) {
		if envKeyPattern.MatchString(key) {
			payload.WriteString("export " + key + "=" + shellQuote(env[key]) + "\n")
		}
	}
	payload.WriteString("\n" + script + "\n")
	return []byte(payload.String())
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
