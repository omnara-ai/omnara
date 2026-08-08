package blaxel

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
)

func TestManagedAwakeProcessSetupScript(t *testing.T) {
	script := managedAwakeProcessSetupScript()
	awakeProcessIndex := strings.Index(script, daemonprotocol.BlaxelLocalAPIURL+"/process")
	reporterIndex := strings.Index(script, "r provider_setup omnara_start_blaxel_awake_process")
	if awakeProcessIndex < 0 || reporterIndex < 0 || awakeProcessIndex >= reporterIndex {
		t.Fatalf("setup script does not define awake setup before running it:\n%s", script)
	}
	for _, value := range []string{
		"omnara_awake_process_name_prefix=" + daemonprotocol.BlaxelAwakeProcessNamePrefix,
		"supervisor_pid=$omnara_supervisor_pid",
		"supervisor_start=$omnara_supervisor_start",
		`"keepAlive":true`,
		`"timeout":0`,
		`"waitForCompletion":false`,
		`"status"[[:space:]]*:[[:space:]]*"running"`,
		`"keepAlive"[[:space:]]*:[[:space:]]*true`,
		`omnara_supervisor_pid=$$`,
		`return 1`,
	} {
		if !strings.Contains(script, value) {
			t.Fatalf("boot script does not contain %q:\n%s", value, script)
		}
	}
	if output, err := exec.Command("/bin/sh", "-n", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("boot script syntax: %v: %s", err, output)
	}
}
