package blaxel

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
)

func TestManagedBootScriptWithAwakeProcess(t *testing.T) {
	managedBoot := "echo managed-boot\nexec /test/omnarad start --no-service"
	script := managedBootScriptWithAwakeProcess(managedBoot)
	awakeProcessIndex := strings.Index(script, daemonprotocol.BlaxelLocalAPIURL+"/process")
	managedBootIndex := strings.Index(script, managedBoot)
	if awakeProcessIndex < 0 || managedBootIndex < 0 || awakeProcessIndex >= managedBootIndex {
		t.Fatalf("boot script does not create the awake process before managed boot:\n%s", script)
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
	} {
		if !strings.Contains(script, value) {
			t.Fatalf("boot script does not contain %q:\n%s", value, script)
		}
	}
	if output, err := exec.Command("/bin/sh", "-n", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("boot script syntax: %v: %s", err, output)
	}
}
