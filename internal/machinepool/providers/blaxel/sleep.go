package blaxel

import (
	_ "embed"
	"strings"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
)

//go:embed awake_process_boot.sh
var awakeProcessBootScript string

func managedAwakeProcessSetupScript() string {
	return "unset omnara_awake_process_name_prefix omnara_blaxel_process_api_url omnara_supervisor_pid\n" +
		"omnara_awake_process_name_prefix=" + daemonprotocol.BlaxelAwakeProcessNamePrefix + "\n" +
		"omnara_blaxel_process_api_url=" + daemonprotocol.BlaxelLocalAPIURL + "/process\n" +
		strings.TrimSpace(awakeProcessBootScript) + "\n" +
		"omnara_supervisor_pid=$$\n" +
		"r provider_setup omnara_start_blaxel_awake_process \"$omnara_supervisor_pid\"\n" +
		"unset omnara_awake_process_name_prefix omnara_blaxel_process_api_url omnara_supervisor_pid"
}
