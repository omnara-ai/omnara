package boxd

import (
	_ "embed"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

// managedBootLauncherScript runs inside the machine through `boxd exec`. It
// reads the boot payload from stdin, starts it detached under its own session,
// and is a no-op when a previous bootstrap is still running, which keeps
// provisioning retries idempotent.
//
//go:embed managed_boot_launcher.sh
var managedBootLauncherScript string

var envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func launcherCommand() string {
	return strings.TrimSpace(managedBootLauncherScript)
}

// bootPayload is the script the launcher executes: boxd does not apply
// CreateVm env vars, so the managed machine environment is exported before
// the shared Omnara boot script runs.
func bootPayload(env map[string]string) ([]byte, error) {
	var payload strings.Builder
	payload.WriteString("#!/bin/sh\n")
	for _, key := range slices.Sorted(maps.Keys(env)) {
		if !envKeyPattern.MatchString(key) {
			return nil, fmt.Errorf("machine env key %q is not a valid shell identifier", key)
		}
		value := env[key]
		if strings.ContainsRune(value, 0) {
			return nil, fmt.Errorf("machine env %s must not contain NUL bytes", key)
		}
		payload.WriteString("export " + key + "=" + shellQuote(value) + "\n")
	}
	payload.WriteString("\n" + providers.ManagedBootScript() + "\n")
	return []byte(payload.String()), nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
