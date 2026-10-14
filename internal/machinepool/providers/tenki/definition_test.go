package tenki

import (
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/require"
)

func TestDefinitionRequiresToken(t *testing.T) {
	_, err := (Definition{}).NewProvider(nil, providers.RuntimeConfig{})
	require.ErrorContains(t, err, "auth token is required")
}

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
	require.Zero(t, options.DiskSizeGB)
	require.EqualValues(t, baseImageDiskSizeGB, options.diskSizeGB())
	config := testProvisioning()
	config.ProviderOptions = map[string]json.RawMessage{"image": json.RawMessage(`"workspace/custom"`)}
	require.ErrorContains(t, d.ValidateMachineProvisioning(policy, config), "not allowed")
	policy.ProviderConfig = json.RawMessage(`{"allowed_images":["workspace/custom"]}`)
	require.NoError(t, d.ValidatePool(policy))
	require.NoError(t, d.ValidateMachineProvisioning(policy, config))
	require.NoError(t, d.ValidateMachineProvisioning(policy, testProvisioning()))
}

func TestDefinitionRejectsInvalidResourcesAndOptions(t *testing.T) {
	for _, mutate := range []func(*executionstore.MachineProvisioningConfig){
		func(c *executionstore.MachineProvisioningConfig) { c.CPU = new(0) },
		func(c *executionstore.MachineProvisioningConfig) { c.MemoryMB = new(1025) },
		func(c *executionstore.MachineProvisioningConfig) {
			c.ProviderOptions = map[string]json.RawMessage{"region": json.RawMessage(`"us"`)}
		},
		func(c *executionstore.MachineProvisioningConfig) {
			c.ProviderOptions = map[string]json.RawMessage{"image": json.RawMessage(`""`)}
		},
		func(c *executionstore.MachineProvisioningConfig) {
			c.ProviderOptions = map[string]json.RawMessage{"image": json.RawMessage(`null`)}
		},
		func(c *executionstore.MachineProvisioningConfig) {
			c.ProviderOptions = map[string]json.RawMessage{"image": json.RawMessage(`"  "`)}
		},
		func(c *executionstore.MachineProvisioningConfig) {
			c.ProviderOptions = map[string]json.RawMessage{"disk_size_gb": json.RawMessage(`101`)}
		},
		func(c *executionstore.MachineProvisioningConfig) {
			c.ProviderOptions = map[string]json.RawMessage{"disk_size_gb": json.RawMessage(`4`)}
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
		`{"api_base_url":"https://api.tenki.cloud"}`,
		`{"workspace":"x"}`,
		`{"allowed_images":[""]}`,
	} {
		_, err := parseProviderConfig(json.RawMessage(raw))
		require.Error(t, err)
	}
}
