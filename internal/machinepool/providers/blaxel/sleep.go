package blaxel

import (
	_ "embed"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
)

//go:embed awake_process_boot.sh
var awakeProcessBootScript string

func managedBootScriptWithAwakeProcess(script string) string {
	return "omnara_awake_process_name_prefix=" + daemonprotocol.BlaxelAwakeProcessNamePrefix + "\n" +
		"omnara_blaxel_process_api_url=" + daemonprotocol.BlaxelLocalAPIURL + "/process\n" +
		awakeProcessBootScript + "\n" + script
}
