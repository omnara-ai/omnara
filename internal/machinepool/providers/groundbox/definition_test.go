package groundbox

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestGroundboxProviderConfig(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, json.RawMessage("null"), json.RawMessage(`{}`)} {
		config, err := parseProviderConfig(raw)
		if err != nil || config.AllowedTemplates != nil {
			t.Fatalf("parse provider config %s = %+v, %v", raw, config, err)
		}
	}
	config, err := parseProviderConfig(json.RawMessage(`{"allowed_templates":[" omnara-box ","tpl_abcdefgh:2"]}`))
	if err != nil || len(config.AllowedTemplates) != 2 || config.AllowedTemplates[0] != "omnara-box" {
		t.Fatalf("parse allowlist = %+v, %v", config, err)
	}
	for name, raw := range map[string]string{
		"unknown field":  `{"api_url":"https://api.example.test"}`,
		"empty list":     `{"allowed_templates":[]}`,
		"mixed wildcard": `{"allowed_templates":["*","omnara-box"]}`,
		"bad template":   `{"allowed_templates":["Omnara Box"]}`,
	} {
		if _, err := parseProviderConfig(json.RawMessage(raw)); err == nil {
			t.Errorf("%s: parse provider config accepted %s", name, raw)
		}
	}
}

func TestGroundboxProviderOptions(t *testing.T) {
	options, err := parseProviderOptions(map[string]json.RawMessage{
		"template":       mustRawJSON(t, " python-ml:3 "),
		"startup_script": mustRawJSON(t, "echo ready"),
		"sleep_after_ms": mustRawJSON(t, 30000),
	})
	if err != nil {
		t.Fatalf("parse provider options: %v", err)
	}
	if options.Template != "python-ml:3" || options.StartupScript != "echo ready" || options.SleepAfterMS != 30000 ||
		options.idleTimeoutSeconds() != idleTimeoutSeconds {
		t.Fatalf("provider options = %+v", options)
	}
	options, err = parseProviderOptions(nil)
	if err != nil || options != (providerOptions{}) || options.idleTimeoutSeconds() != 0 {
		t.Fatalf("empty provider options = %+v, %v", options, err)
	}
	for name, raw := range map[string]map[string]json.RawMessage{
		"empty template":  {"template": mustRawJSON(t, " ")},
		"bad template":    {"template": mustRawJSON(t, "Python ML")},
		"zero version":    {"template": mustRawJSON(t, "python-ml:0")},
		"unknown option":  {"image": mustRawJSON(t, "python:3.12")},
		"short sleep":     {"sleep_after_ms": mustRawJSON(t, 29999)},
		"string sleep":    {"sleep_after_ms": mustRawJSON(t, "30000")},
		"huge script":     {"startup_script": mustRawJSON(t, strings.Repeat("x", 64*1024+1))},
		"wrong type json": {"template": json.RawMessage(`3`)},
	} {
		if _, err := parseProviderOptions(raw); err == nil {
			t.Errorf("%s: parse provider options accepted %v", name, raw)
		}
	}
}

func TestGroundboxMachineSizes(t *testing.T) {
	for _, size := range [][2]int{{1, 1024}, {1, 4096}, {2, 4096}, {16, 65536}} {
		cpu, memoryMB := size[0], size[1]
		if _, err := providerOptionsFromProvisioning(executionstore.MachineProvisioningConfig{
			CPU: &cpu, MemoryMB: &memoryMB,
		}); err != nil {
			t.Errorf("%d vCPU %d MB rejected: %v", cpu, memoryMB, err)
		}
	}
	for _, size := range [][2]int{{0, 1024}, {17, 4096}, {1, 512}, {2, 3000}, {1, 5120}} {
		cpu, memoryMB := size[0], size[1]
		if _, err := providerOptionsFromProvisioning(executionstore.MachineProvisioningConfig{
			CPU: &cpu, MemoryMB: &memoryMB,
		}); err == nil {
			t.Errorf("%d vCPU %d MB accepted", cpu, memoryMB)
		}
	}
	if _, err := providerOptionsFromProvisioning(executionstore.MachineProvisioningConfig{}); err == nil {
		t.Error("a machine without a size was accepted")
	}
}

func TestGroundboxDefinitionValidatesPoolsAndMachines(t *testing.T) {
	definition := Definition{}
	policy := testPolicy(t, map[string]any{}, map[string]any{})
	if err := definition.ValidatePool(policy); err != nil {
		t.Fatalf("validate pool: %v", err)
	}
	machine := testProvisioning(t, map[string]any{"template": "other-box"})
	if err := definition.ValidateMachineProvisioning(policy, machine); err == nil {
		t.Fatal("a template outside the pool default was accepted without an allowlist")
	}
	allowed := testPolicy(t, map[string]any{"allowed_templates": []string{"other-box"}}, map[string]any{})
	if err := definition.ValidateMachineProvisioning(allowed, machine); err != nil {
		t.Fatalf("allowlisted template rejected: %v", err)
	}
	wildcard := testPolicy(t, map[string]any{"allowed_templates": []string{"*"}}, map[string]any{})
	intent, err := definition.BuildMachineProvisioningIntent(wildcard, machine)
	if err != nil || intent.CPU == nil || *intent.CPU != 2 || intent.MemoryMB == nil || *intent.MemoryMB != 4096 {
		t.Fatalf("intent = %+v, %v; want the configured size kept", intent, err)
	}
	if err := definition.ValidateMachineProvisioning(wildcard, testProvisioning(t, nil)); err != nil {
		t.Fatalf("machine on the default image rejected: %v", err)
	}
	noDefaultSize := policy
	noDefaultSize.DefaultProvisioning.CPU = nil
	if err := definition.ValidatePool(noDefaultSize); err == nil {
		t.Fatal("a pool without a default cpu was accepted")
	}
}

func TestGroundboxDefinitionContract(t *testing.T) {
	definition := Definition{}
	policy := definition.ResourcePolicy()
	if policy.CPU.Provisioning != providers.MachineResourceConfigured ||
		policy.MemoryMB.Provisioning != providers.MachineResourceConfigured {
		t.Fatalf("resource policy = %+v, want configured cpu and memory", policy)
	}
	if _, err := definition.NewProvider(nil, providers.RuntimeConfig{}); err == nil {
		t.Fatal("a provider without an API key was created")
	}
	if _, err := definition.NewProvider(
		json.RawMessage(`{"unknown":true}`),
		providers.RuntimeConfig{ProviderAuthToken: "gbx_test"},
	); err == nil {
		t.Fatal("a provider with an invalid config was created")
	}
	runtimeProvider, err := definition.NewRuntimeProvider(nil, providers.RuntimeConfig{ProviderAuthToken: " gbx_test "})
	if err != nil || runtimeProvider == nil {
		t.Fatalf("new runtime provider = %v, %v", runtimeProvider, err)
	}
	merged := definition.ResolveMachineProviderOptions(
		map[string]json.RawMessage{"template": json.RawMessage(`"a"`), "sleep_after_ms": json.RawMessage(`30000`)},
		map[string]json.RawMessage{"template": json.RawMessage(`"b"`)},
		nil,
	)
	if string(merged["template"]) != `"b"` || string(merged["sleep_after_ms"]) != "30000" {
		t.Fatalf("merged options = %v", merged)
	}
}
