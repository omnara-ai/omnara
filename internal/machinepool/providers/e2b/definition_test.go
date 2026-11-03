package e2b

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/require"
)

func TestNewProviderUsesTheConfiguredDomain(t *testing.T) {
	_, err := newProvider(nil, providers.RuntimeConfig{ProviderAuthToken: " "})
	require.ErrorContains(t, err, "auth token is required")
	for _, test := range []struct {
		config any
		domain string
	}{
		{config: map[string]any{}, domain: "e2b.app"},
		{config: map[string]any{"domain": " e2b-juliett.dev "}, domain: "e2b-juliett.dev"},
	} {
		p, err := newProvider(rawJSON(t, test.config), providers.RuntimeConfig{ProviderAuthToken: "key"})
		require.NoError(t, err)
		client, ok := p.api.(*restClient)
		require.True(t, ok)
		require.Equal(t, test.domain, client.domain)
	}
	for _, config := range []any{
		map[string]any{"domain": "localhost"},
		map[string]any{"domain": "https://e2b.app"},
		map[string]any{"domain": "e2b.app/evil"},
		map[string]any{"api_base_url": "https://api.e2b.app"},
	} {
		_, err := newProvider(rawJSON(t, config), providers.RuntimeConfig{ProviderAuthToken: "key"})
		require.Error(t, err, "%v", config)
	}
}

func TestParseProviderOptionsValidatesTemplates(t *testing.T) {
	for _, valid := range []string{"claude", "e2b/claude", "acme/my_template-1", "rki5dems9wqfm4r03t7g"} {
		options, err := parseProviderOptions(testOptions(t, " "+valid+" "))
		require.NoError(t, err, valid)
		require.Equal(t, valid, options.Template)
	}
	for _, invalid := range []string{"", "claude:v1", "Claude", "a/b/c", "*"} {
		_, err := parseProviderOptions(testOptions(t, invalid))
		require.Error(t, err, invalid)
	}
	_, err := parseProviderOptions(nil)
	require.ErrorContains(t, err, "requires provider_options")
	for _, extra := range []map[string]json.RawMessage{
		{"sleep_after_ms": json.RawMessage(`60000`)},
		{"startup_script": rawJSON(t, strings.Repeat("x", 64*1024+1))},
	} {
		options := testOptions(t, "claude")
		for key, value := range extra {
			options[key] = value
		}
		_, err := parseProviderOptions(options)
		require.Error(t, err)
	}
}

func TestDefinitionAllowlistsTemplates(t *testing.T) {
	definition := Definition{}
	require.NoError(t, definition.ValidatePool(testPolicy(t, map[string]any{}, "claude")))
	machine := executionstore.MachineProvisioningConfig{ProviderOptions: testOptions(t, "codex")}
	require.ErrorContains(
		t,
		definition.ValidateMachineProvisioning(testPolicy(t, map[string]any{}, "claude"), machine),
		"allowed_templates",
	)
	allowAll := testPolicy(t, map[string]any{"allowed_templates": []string{"*"}}, "claude")
	require.NoError(t, definition.ValidateMachineProvisioning(allowAll, machine))
	_, err := Definition{}.BuildMachineProvisioningIntent(
		testPolicy(t, map[string]any{"allowed_templates": []string{"claude:v1"}}, "claude"),
		machine,
	)
	require.Error(t, err)
}

func TestBuildMachineProvisioningIntentLeavesSizeToTheTemplate(t *testing.T) {
	cpu, memoryMB := 4, 4096
	intent, err := Definition{}.BuildMachineProvisioningIntent(
		testPolicy(t, map[string]any{}, "claude"),
		executionstore.MachineProvisioningConfig{CPU: &cpu, MemoryMB: &memoryMB, ProviderOptions: testOptions(t, "claude")},
	)
	require.NoError(t, err)
	require.Nil(t, intent.CPU)
	require.Nil(t, intent.MemoryMB)
	policy := testPolicy(t, map[string]any{}, "claude")
	policy.ResourceLimits.MaxMachineCPU = nil
	require.ErrorContains(t, Definition{}.ValidatePool(policy), "max_machine_cpu")
}
