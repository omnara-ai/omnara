package blaxel

import (
	_ "embed"
	"strconv"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
)

//go:embed awake_process_boot.sh
var awakeProcessBootScript string

//go:embed wake_process.sh
var wakeProcessScript string

func managedBootScriptWithAwakeProcess(script string) string {
	return "omnara_awake_process_name_prefix=" + daemonprotocol.BlaxelAwakeProcessNamePrefix + "\n" +
		"omnara_blaxel_process_api_url=" + daemonprotocol.BlaxelLocalAPIURL + "/process\n" +
		awakeProcessBootScript + "\n" + script
}

func wakeProcessCommand() string {
	return "omnara_wake_listener_url=http://127.0.0.1:" +
		strconv.Itoa(daemonprotocol.WakeListenerPort) + "/\n" + wakeProcessScript
}
