package bead

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/require"
)

func TestBeadProviderConfig(t *testing.T) {
	_, err := newProvider(nil, providers.RuntimeConfig{})
	require.ErrorContains(t, err, "auth token")
	for _, apiURL := range []string{"", "https://api.example.com/"} {
		p, err := newProvider(
			rawJSON(t, map[string]any{"api_url": apiURL}), providers.RuntimeConfig{ProviderAuthToken: "key"},
		)
		require.NoError(t, err)
		client, ok := p.api.(*restClient)
		require.True(t, ok)
		if apiURL == "" {
			require.Equal(t, defaultAPIBaseURL, client.apiBaseURL)
		} else {
			require.Equal(t, "https://api.example.com", client.apiBaseURL)
		}
	}
	for _, apiURL := range []string{
		"http://api.example.com", "https://", "https://key@api.example.com",
		"https://api.example.com/path", "https://api.example.com?key=x", "https://api.example.com#x",
	} {
		_, err := parseProviderConfig(rawJSON(t, map[string]any{"api_url": apiURL}))
		require.Error(t, err, apiURL)
	}
	_, err = parseProviderConfig(json.RawMessage(`{"unknown":true}`))
	require.Error(t, err)
}

func TestBeadDefinitionTemplatePolicy(t *testing.T) {
	definition := Definition{}
	policy := testPolicy(t, map[string]any{}, "base")
	require.NoError(t, definition.ValidatePool(policy))
	machine := executionstore.MachineProvisioningConfig{ProviderOptions: testOptions(t, "ci")}
	require.ErrorContains(t, definition.ValidateMachineProvisioning(policy, machine), "allowed_templates")
	policy.ProviderConfig = rawJSON(t, map[string]any{"allowed_templates": []string{"base", "ci"}})
	require.NoError(t, definition.ValidateMachineProvisioning(policy, machine))
	intent, err := definition.BuildMachineProvisioningIntent(policy, machine)
	require.NoError(t, err)
	require.Nil(t, intent.CPU)
	require.Nil(t, intent.MemoryMB)
	options, err := parseProviderOptions(nil)
	require.NoError(t, err)
	require.Equal(t, "base", options.template())
	for _, raw := range []map[string]json.RawMessage{
		testOptions(t, ""), testOptions(t, "*"), {"unknown": json.RawMessage(`true`)},
		{"startup_script": json.RawMessage(`123`)},
	} {
		_, err := parseProviderOptions(raw)
		require.Error(t, err)
	}
}

func TestBeadPrepareProvisioningRoundsTemplateToPublishedShape(t *testing.T) {
	for _, name := range []string{"base", "alias"} {
		api := &fakeAPI{templates: []template{{TemplateID: "base", Names: []string{"alias"}, CPUCount: 2, MemoryMB: 2048}}}
		facts, err := testProvider(api).PrepareProvisioning(context.Background(),
			executionstore.MachineProvisioningConfig{ProviderOptions: testOptions(t, name)})
		require.NoError(t, err)
		require.Equal(t, 2, *facts.CPU)
		require.Equal(t, 4096, *facts.MemoryMB)
	}
	api := &fakeAPI{}
	_, err := testProvider(api).PrepareProvisioning(t.Context(), executionstore.MachineProvisioningConfig{})
	require.ErrorContains(t, err, "not found")
	api.templateErr = errors.New("offline")
	_, err = testProvider(api).PrepareProvisioning(t.Context(), executionstore.MachineProvisioningConfig{})
	require.ErrorIs(t, err, api.templateErr)
	api.templates = []template{{TemplateID: "base"}}
	api.templateErr = nil
	_, err = testProvider(api).PrepareProvisioning(t.Context(), executionstore.MachineProvisioningConfig{})
	require.ErrorContains(t, err, "no ready size")
	facts, err := testProvider(api).PrepareProvisioning(t.Context(), testProvisioning(t))
	require.NoError(t, err)
	require.Equal(t, 4096, *facts.MemoryMB)
}

func TestBeadPrepareProvisioningShapeFailures(t *testing.T) {
	api := &fakeAPI{templates: []template{{TemplateID: "base", CPUCount: 2, MemoryMB: 2048}}}
	api.shapeErr = errors.New("pricing offline")
	_, err := testProvider(api).PrepareProvisioning(t.Context(), executionstore.MachineProvisioningConfig{})
	require.ErrorIs(t, err, api.shapeErr)
	api.shapeErr = nil
	api.shapes = []machineShape{{CPU: 1, MemoryMB: 2048}}
	_, err = testProvider(api).PrepareProvisioning(t.Context(), executionstore.MachineProvisioningConfig{})
	require.ErrorContains(t, err, "no bead shape fits")
	api.shapes = []machineShape{{CPU: 4, MemoryMB: 8192}, {CPU: 2, MemoryMB: 8192}, {CPU: 2, MemoryMB: 4096}}
	facts, err := testProvider(api).PrepareProvisioning(t.Context(), executionstore.MachineProvisioningConfig{})
	require.NoError(t, err)
	require.Equal(t, 2, *facts.CPU)
	require.Equal(t, 4096, *facts.MemoryMB)
}
