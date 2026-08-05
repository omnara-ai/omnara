package providers

import (
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/omnara-ai/omnara/internal/machinedaemon/localstore"
)

//go:embed managed_machine_startup.sh
var managedMachineStartupScriptBody string

//go:embed managed_daemon_launcher.sh
var managedDaemonLauncherScriptBody string

const (
	ManagedBootstrapScriptEnvVar = "OMNARA_BOOTSTRAP_SCRIPT"
	startupScriptEnvVar          = "OMNARA_STARTUP_SCRIPT_PAYLOAD"
	managedDaemonSeedPath        = "/usr/local/bin/omnarad"
	maxManagedStartupScriptBytes = 64 * 1024
)

func BuildManagedMachineEnv(
	omnaraPublicURL string,
	machineToken string,
	startupScript string,
	machineEnv map[string]string,
) (map[string]string, error) {
	omnaraPublicURL = strings.TrimRight(strings.TrimSpace(omnaraPublicURL), "/")
	if omnaraPublicURL == "" {
		return nil, errors.New("public URL is required for managed machine bootstrap")
	}
	env := make(map[string]string, len(machineEnv)+3)
	for key, value := range machineEnv {
		if strings.HasPrefix(strings.ToUpper(key), "OMNARA_") {
			return nil, fmt.Errorf("machine env cannot set reserved OMNARA_ key %s", key)
		}
		env[key] = value
	}
	env["OMNARA_API_URL"] = omnaraPublicURL
	env["OMNARA_MACHINE_TOKEN"] = machineToken
	if startupScript != "" {
		env[startupScriptEnvVar] = base64.StdEncoding.EncodeToString([]byte(startupScript))
	}
	return env, nil
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

func ManagedBootScript(startupScript string) string {
	if startupScript == "" {
		return managedDaemonLauncherScript()
	}
	return managedMachineStartupScript() + "\n\n" + managedDaemonLauncherScript()
}

func ManagedDaemonLauncherArgs() []string {
	return []string{
		"/bin/sh",
		"-c",
		`original_umask=$(umask);umask${IFS}077;bootstrap=/tmp/omnarad-bootstrap;printf${IFS}%s${IFS}${OMNARA_BOOTSTRAP_SCRIPT:?}|base64${IFS}-d>$bootstrap&&unset${IFS}OMNARA_BOOTSTRAP_SCRIPT&&umask${IFS}"$original_umask"&&exec${IFS}/bin/sh${IFS}$bootstrap`,
	}
}

func managedMachineStartupScript() string {
	return strings.TrimSpace(managedMachineStartupScriptBody)
}

func managedDaemonLauncherScript() string {
	return "omnara_daemon_home_dir=" + localstore.DefaultDirName + "\n" +
		"omnara_daemon_seed_path=" + managedDaemonSeedPath + "\n" +
		strings.TrimSpace(managedDaemonLauncherScriptBody)
}
