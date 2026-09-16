package tenki

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/require"
)

func testPolicy() executionstore.MachinePoolProviderPolicy {
	return executionstore.MachinePoolProviderPolicy{
		DefaultProvisioning: testProvisioning(),
		ResourceLimits: executionstore.MachineResourceLimits{
			MaxTotalCPU:        new(4),
			MaxTotalMemoryMB:   new(8192),
			MaxMachineCPU:      new(2),
			MaxMachineMemoryMB: new(4096),
		},
	}
}

func TestDefinitionDefaultsAndAllowlist(t *testing.T) {
	d := Definition{}
	policy := testPolicy()
	require.NoError(t, d.ValidatePool(policy))
	options, err := parseProviderOptions(nil)
	require.NoError(t, err)
	require.Empty(t, options.Image)
	require.EqualValues(t, 20, options.DiskSizeGB)
	config := testProvisioning()
	config.ProviderOptions = map[string]json.RawMessage{"image": json.RawMessage(`"workspace/custom"`)}
	require.ErrorContains(t, d.ValidateMachineProvisioning(policy, config), "not allowed")
	policy.ProviderConfig = json.RawMessage(`{"allowed_images":["", "workspace/custom"]}`)
	require.NoError(t, d.ValidateMachineProvisioning(policy, config))
}

func TestDefinitionRejectsInvalidResourcesAndOptions(t *testing.T) {
	for _, mutate := range []func(*executionstore.MachineProvisioningConfig){
		func(c *executionstore.MachineProvisioningConfig) { c.CPU = new(17) },
		func(c *executionstore.MachineProvisioningConfig) { c.MemoryMB = new(511) },
		func(c *executionstore.MachineProvisioningConfig) { c.MemoryMB = new(1025) },
		func(c *executionstore.MachineProvisioningConfig) {
			c.ProviderOptions = map[string]json.RawMessage{"region": json.RawMessage(`"us"`)}
		},
		func(c *executionstore.MachineProvisioningConfig) {
			c.ProviderOptions = map[string]json.RawMessage{"disk_size_gb": json.RawMessage(`101`)}
		},
		func(c *executionstore.MachineProvisioningConfig) {
			c.ProviderOptions = map[string]json.RawMessage{"disk_size_gb": json.RawMessage(`"20"`)}
		},
	} {
		config := testProvisioning()
		mutate(&config)
		_, err := providerOptionsFromProvisioning(config)
		require.Error(t, err)
	}
	for _, raw := range []string{
		`{"api_base_url":"http://example.com"}`,
		`{"api_base_url":"https://user:key@example.com"}`,
		`{"api_base_url":"https://example.com?key=x"}`,
		`{"workspace":"x"}`,
	} {
		_, err := parseProviderConfig(json.RawMessage(raw))
		require.Error(t, err)
	}
}

func TestBootstrapShellSyntaxAndEnvironmentQuoting(t *testing.T) {
	value := "quotes'\"\n$(touch /should-not-exist) `false`"
	script, err := bootstrapScript(map[string]string{"VALUE": value})
	require.NoError(t, err)
	cmd := exec.CommandContext(t.Context(), "/bin/sh", "-n")
	cmd.Stdin = strings.NewReader(script)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
	env, err := environmentScript(map[string]string{"VALUE": value})
	require.NoError(t, err)
	cmd = exec.CommandContext(t.Context(), "/bin/sh", "-s")
	cmd.Stdin = strings.NewReader(env + "printf '%s' \"$VALUE\"")
	output, err = cmd.CombinedOutput()
	require.NoError(t, err)
	require.Equal(t, value, string(output))
	_, err = environmentScript(map[string]string{"BAD;exit": "x"})
	require.Error(t, err)
}
