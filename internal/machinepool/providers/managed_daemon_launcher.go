package providers

import (
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/omnara-ai/omnara/internal/envname"
	"github.com/omnara-ai/omnara/internal/machinedaemon/localstore"
)

//go:embed managed_bootstrap_prelude.sh
var managedBootstrapPreludeScriptBody string

//go:embed managed_startup.sh
var managedStartupScriptBody string

//go:embed managed_scoped_prelude.sh
var managedScopedPreludeScriptBody string

//go:embed managed_scoped_startup.sh
var managedScopedStartupScriptBody string

//go:embed managed_daemon_launcher.sh
var managedDaemonLauncherScriptBody string

const (
	ManagedBootstrapScriptEnvVar = "OMNARA_BOOTSTRAP_SCRIPT"
	startupScriptEnvVar          = "OMNARA_STARTUP_SCRIPT_PAYLOAD"
	startupEnvFileEnvVar         = "OMNARA_STARTUP_ENV_FILE"
	daemonEnvKeysEnvVar          = "OMNARA_DAEMON_ENV_KEYS"
	// Bump this when an already-running scoped command is no longer safe to adopt.
	managedScopedBootScriptHeader = "# omnara-managed-scoped-bootstrap:v1"
	managedDaemonSeedPath         = "/usr/local/bin/omnarad"
	maxManagedStartupScriptBytes  = 64 * 1024
	managedDaemonLauncherCommand  = `m=$(umask);` +
		`umask${IFS}077;` +
		`b=/tmp/omnarad-bootstrap;` +
		`printf${IFS}%s${IFS}${OMNARA_BOOTSTRAP_SCRIPT:?}` +
		`|base64${IFS}-d>$b` +
		`&&unset${IFS}OMNARA_BOOTSTRAP_SCRIPT` +
		`&&umask${IFS}"$m"` +
		`&&exec${IFS}/bin/sh${IFS}$b`
)

type ManagedBootEnvironment struct {
	DaemonEnv  map[string]string
	StartupEnv map[string]string
}

func BuildManagedBootEnvironment(
	omnaraPublicURL string,
	machineToken string,
	startupScript string,
	machineEnv map[string]string,
) (ManagedBootEnvironment, error) {
	omnaraPublicURL = strings.TrimRight(strings.TrimSpace(omnaraPublicURL), "/")
	if omnaraPublicURL == "" {
		return ManagedBootEnvironment{}, errors.New("public URL is required for managed machine bootstrap")
	}
	startupEnv := make(map[string]string, len(machineEnv))
	for key, value := range machineEnv {
		if err := validateManagedEnvEntry("machine env", key, value); err != nil {
			return ManagedBootEnvironment{}, err
		}
		startupEnv[key] = value
	}
	daemonEnv := map[string]string{
		"OMNARA_API_URL":       omnaraPublicURL,
		"OMNARA_MACHINE_TOKEN": machineToken,
	}
	if startupScript != "" {
		daemonEnv[startupScriptEnvVar] = base64.StdEncoding.EncodeToString([]byte(startupScript))
	}
	return ManagedBootEnvironment{DaemonEnv: daemonEnv, StartupEnv: startupEnv}, nil
}

func (environment ManagedBootEnvironment) CombinedEnv() map[string]string {
	// Compatibility transport for providers without scoped bootstrap delivery.
	// The startup script and daemon both inherit the combined environment.
	env := make(map[string]string, len(environment.StartupEnv)+len(environment.DaemonEnv))
	maps.Copy(env, environment.StartupEnv)
	maps.Copy(env, environment.DaemonEnv)
	return env
}

func (environment ManagedBootEnvironment) ScopedDaemonEnv(
	startupEnvFile string,
) (map[string]string, error) {
	env := maps.Clone(environment.DaemonEnv)
	if env == nil {
		env = map[string]string{}
	}
	if _, hasStartupScript := env[startupScriptEnvVar]; !hasStartupScript {
		if startupEnvFile != "" {
			return nil, errors.New("startup environment file requires a startup script")
		}
		return env, nil
	}
	if startupEnvFile == "" || strings.ContainsRune(startupEnvFile, 0) {
		return nil, errors.New("startup environment file path is required")
	}
	env[startupEnvFileEnvVar] = startupEnvFile
	keys := slices.Sorted(maps.Keys(environment.DaemonEnv))
	env[daemonEnvKeysEnvVar] = strings.Join(keys, " ")
	return env, nil
}

func RenderManagedStartupEnvironment(env map[string]string) (string, error) {
	var script strings.Builder
	for _, key := range slices.Sorted(maps.Keys(env)) {
		value := env[key]
		if err := validateManagedEnvEntry("startup env", key, value); err != nil {
			return "", err
		}
		fmt.Fprintf(&script, "export %s=%s\n", key, shellSingleQuote(value))
	}
	return script.String(), nil
}

func validateManagedEnvEntry(what, key, value string) error {
	if !envname.Valid(key) {
		return fmt.Errorf("%s key %q must match %s", what, key, envname.Pattern)
	}
	if strings.HasPrefix(strings.ToUpper(key), "OMNARA_") {
		return fmt.Errorf("%s cannot set reserved OMNARA_ key %s", what, key)
	}
	if strings.ContainsRune(value, 0) {
		return fmt.Errorf("%s %s cannot contain NUL", what, key)
	}
	return nil
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func ValidateManagedStartupScript(what, startupScript string) error {
	if len(startupScript) > maxManagedStartupScriptBytes {
		return fmt.Errorf(
			"%s startup_script must be at most %d bytes",
			what,
			maxManagedStartupScriptBytes,
		)
	}
	return nil
}

func ManagedBootScript() string {
	return strings.Join([]string{
		managedBootstrapPreludeScript(),
		managedStartupScript(),
		managedDaemonLauncherScript(),
	}, "\n\n")
}

func ManagedScopedBootScript(providerSetup string) string {
	parts := []string{
		managedScopedBootScriptHeader,
		managedBootstrapPreludeScript(),
		managedScopedPreludeScript(),
	}
	if providerSetup = strings.TrimSpace(providerSetup); providerSetup != "" {
		parts = append(parts, providerSetup)
	}
	parts = append(parts, managedScopedStartupScript(), managedDaemonLauncherScript())
	return strings.Join(parts, "\n\n")
}

func IsManagedScopedBootScript(command string) bool {
	return strings.HasPrefix(command, managedScopedBootScriptHeader+"\n")
}

func ManagedBootScriptPayload() string {
	return base64.StdEncoding.EncodeToString([]byte(ManagedBootScript()))
}

func ManagedDaemonLauncherArgs() []string {
	return []string{
		"/bin/sh",
		"-c",
		managedDaemonLauncherCommand,
	}
}

func managedBootstrapPreludeScript() string {
	return strings.TrimSpace(managedBootstrapPreludeScriptBody)
}

func managedStartupScript() string {
	return strings.TrimSpace(managedStartupScriptBody)
}

func managedScopedPreludeScript() string {
	return strings.TrimSpace(managedScopedPreludeScriptBody)
}

func managedScopedStartupScript() string {
	return strings.TrimSpace(managedScopedStartupScriptBody)
}

func managedDaemonLauncherScript() string {
	return "n=" + localstore.DefaultDirName + "\n" +
		"b=" + managedDaemonSeedPath + "\n" +
		strings.TrimSpace(managedDaemonLauncherScriptBody)
}
