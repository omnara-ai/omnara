package providers

import (
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/omnara-ai/omnara/internal/machinedaemon/localstore"
)

//go:embed managed_bootstrap_prelude.sh
var managedBootstrapPreludeScriptBody string

//go:embed managed_daemon_launcher.sh
var managedDaemonLauncherScriptBody string

const (
	ManagedBootstrapScriptEnvVar = "OMNARA_BOOTSTRAP_SCRIPT"
	startupScriptEnvVar          = "OMNARA_STARTUP_SCRIPT_PAYLOAD"
	managedDaemonSeedPath        = "/usr/local/bin/omnarad"
	maxManagedStartupScriptBytes = 64 * 1024
	managedDaemonLauncherCommand = `m=$(umask);` +
		`umask${IFS}077;` +
		`b=/tmp/omnarad-bootstrap;` +
		`printf${IFS}%s${IFS}${OMNARA_BOOTSTRAP_SCRIPT:?}` +
		`|base64${IFS}-d>$b` +
		`&&unset${IFS}OMNARA_BOOTSTRAP_SCRIPT` +
		`&&umask${IFS}"$m"` +
		`&&exec${IFS}/bin/sh${IFS}$b`
)

func BuildManagedMachineEnv(
	omnaraAPIURL string,
	machineToken string,
	startupScript string,
	machineEnv map[string]string,
) (map[string]string, error) {
	apiURL := strings.TrimRight(strings.TrimSpace(omnaraAPIURL), "/")
	if apiURL == "" {
		return nil, errors.New("API URL is required for managed machine bootstrap")
	}
	parsedAPIURL, err := url.Parse(apiURL)
	if err != nil || parsedAPIURL.Opaque != "" || parsedAPIURL.Scheme == "" || parsedAPIURL.Host == "" {
		return nil, errors.New("API URL for managed machine bootstrap must be absolute")
	}
	if parsedAPIURL.Scheme != "http" && parsedAPIURL.Scheme != "https" {
		return nil, errors.New("API URL for managed machine bootstrap must use http or https")
	}
	if parsedAPIURL.User != nil || parsedAPIURL.RawQuery != "" || parsedAPIURL.ForceQuery || parsedAPIURL.Fragment != "" {
		return nil, errors.New("API URL for managed machine bootstrap must not contain credentials, a query, or a fragment")
	}
	parsedAPIURL.Path = "/install/omnarad.sh"
	parsedAPIURL.RawPath = ""
	parsedAPIURL.RawQuery = ""
	parsedAPIURL.Fragment = ""
	installerURL := parsedAPIURL.String()
	env := make(map[string]string, len(machineEnv)+4)
	for key, value := range machineEnv {
		if strings.HasPrefix(strings.ToUpper(key), "OMNARA_") {
			return nil, fmt.Errorf("machine env cannot set reserved OMNARA_ key %s", key)
		}
		env[key] = value
	}
	env["OMNARA_API_URL"] = apiURL
	env["OMNARA_INSTALLER_URL"] = installerURL
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

func ManagedBootScript() string {
	return managedBootstrapPreludeScript() + "\n\n" + managedDaemonLauncherScript()
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

func managedDaemonLauncherScript() string {
	return "n=" + localstore.DefaultDirName + "\n" +
		"b=" + managedDaemonSeedPath + "\n" +
		strings.TrimSpace(managedDaemonLauncherScriptBody)
}
