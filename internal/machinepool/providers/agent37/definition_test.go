package agent37

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestAgent37ProviderConfigAcceptsEmptyConfig(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, json.RawMessage("null"), json.RawMessage(`{}`)} {
		config, err := parseProviderConfig(raw)
		if err != nil {
			t.Fatalf("parse provider config %s: %v", raw, err)
		}
		if config.AllowedTemplates != nil {
			t.Fatalf("provider config = %+v, want no allowlist", config)
		}
	}
}

func TestAgent37ProviderConfigRejectsUnknownAndInvalidFields(t *testing.T) {
	for name, raw := range map[string]string{
		"api base url":   `{"api_base_url":"https://api.example.test"}`,
		"empty list":     `{"allowed_templates":[]}`,
		"mixed wildcard": `{"allowed_templates":["*","acme-agent-box"]}`,
		"empty entry":    `{"allowed_templates":[" "]}`,
	} {
		if _, err := parseProviderConfig(json.RawMessage(raw)); err == nil {
			t.Errorf("%s: parse provider config accepted %s", name, raw)
		}
	}
}

func TestAgent37ProviderOptionsDefaultTheTemplate(t *testing.T) {
	options, err := parseProviderOptions(map[string]json.RawMessage{
		"template":       mustRawJSON(t, " acme-agent-box@2 "),
		"disk_gb":        mustRawJSON(t, 12),
		"startup_script": mustRawJSON(t, "echo ready"),
	})
	if err != nil {
		t.Fatalf("parse provider options: %v", err)
	}
	if options.Template != "acme-agent-box@2" || options.DiskGB != 12 || options.StartupScript != "echo ready" {
		t.Fatalf("provider options = %+v, want the trimmed template, disk, and script", options)
	}
	for name, raw := range map[string]map[string]json.RawMessage{
		"nil":         nil,
		"no template": {"startup_script": mustRawJSON(t, "echo ready")},
	} {
		options, err := parseProviderOptions(raw)
		if err != nil {
			t.Fatalf("%s: parse provider options: %v", name, err)
		}
		if options.Template != "" || options.template() != defaultTemplate {
			t.Fatalf("%s: provider options = %+v, want agent37-omnara by default", name, options)
		}
	}
	for name, raw := range map[string]map[string]json.RawMessage{
		"blank template":    {"template": json.RawMessage(`"  "`)},
		"wildcard template": {"template": json.RawMessage(`"*"`)},
		"newline template":  {"template": json.RawMessage(`"a\nb"`)},
		"unknown key":       {"template": mustRawJSON(t, testTemplate), "image": json.RawMessage(`"ubuntu"`)},
		"zero disk":         {"template": mustRawJSON(t, testTemplate), "disk_gb": json.RawMessage(`0`)},
		"huge disk":         {"template": mustRawJSON(t, testTemplate), "disk_gb": json.RawMessage(`81`)},
		"fractional disk":   {"template": mustRawJSON(t, testTemplate), "disk_gb": json.RawMessage(`4.5`)},
		"long script":       testOptions(t, testTemplate, strings.Repeat("a", 64*1024+1)),
	} {
		if _, err := parseProviderOptions(raw); err == nil {
			t.Errorf("%s: parse provider options accepted %v", name, raw)
		}
	}
}

func TestAgent37MachinesUseTheSellableShapes(t *testing.T) {
	for _, shape := range [][2]int{{2, 4096}, {4, 8192}, {4, 16384}, {4, 32768}} {
		provisioning := testMachineProvisioning(t, testTemplate, shape[0], shape[1])
		if _, err := providerOptionsFromProvisioning(provisioning); err != nil {
			t.Errorf("%d vCPU / %d MB: %v", shape[0], shape[1], err)
		}
	}
	for _, shape := range [][2]int{{1, 1024}, {2, 8192}, {4, 4096}, {8, 16384}, {4, 8000}} {
		_, err := providerOptionsFromProvisioning(testMachineProvisioning(t, testTemplate, shape[0], shape[1]))
		if err == nil || !strings.Contains(err.Error(), "agent37 machines must be") {
			t.Errorf("%d vCPU / %d MB error = %v, want the shapes listed", shape[0], shape[1], err)
		}
	}
	if _, err := providerOptionsFromProvisioning(executionstore.MachineProvisioningConfig{
		ProviderOptions: testOptions(t, testTemplate, ""),
	}); err == nil {
		t.Fatal("provider options accepted a machine without cpu and memory")
	}
}

func TestAgent37ValidatePoolEnforcesShapeTemplateAndAllowlist(t *testing.T) {
	definition := Definition{}
	config := mustRawJSON(t, map[string]any{"allowed_templates": []string{testTemplate}})
	if err := definition.ValidatePool(testPoolPolicy(t, config, testTemplate)); err != nil {
		t.Fatalf("validate pool: %v", err)
	}
	if err := definition.ValidatePool(testPoolPolicy(t, config, "acme-other")); err == nil {
		t.Fatal("validate pool accepted a default template outside the allowlist")
	}
	if err := definition.ValidatePool(testPoolPolicy(t, json.RawMessage(`{}`), "")); err != nil {
		t.Fatalf("validate pool without a template: %v", err)
	}
	if err := definition.ValidatePool(testPoolPolicy(t, config, "")); err == nil {
		t.Fatal("validate pool accepted the default template outside the allowlist")
	}
	policy := testPoolPolicy(t, json.RawMessage(`{}`), testTemplate)
	memoryMB := 6144
	policy.DefaultProvisioning.MemoryMB = &memoryMB
	if err := definition.ValidatePool(policy); err == nil {
		t.Fatal("validate pool accepted a default size Agent37 does not sell")
	}
	policy = testPoolPolicy(t, json.RawMessage(`{}`), testTemplate)
	policy.ResourceLimits.MaxMachineCPU = nil
	if err := definition.ValidatePool(policy); err == nil {
		t.Fatal("validate pool accepted a missing cpu limit")
	}
}

func TestAgent37ValidateMachineProvisioningDefaultsAllowlistToPoolTemplate(t *testing.T) {
	definition := Definition{}
	policy := testPoolPolicy(t, json.RawMessage(`{}`), testTemplate)
	if err := definition.ValidateMachineProvisioning(
		policy,
		testMachineProvisioning(t, testTemplate, 4, 16384),
	); err != nil {
		t.Fatalf("validate machine provisioning: %v", err)
	}
	other := testMachineProvisioning(t, "acme-other", 2, 4096)
	if err := definition.ValidateMachineProvisioning(policy, other); err == nil {
		t.Fatal("validate machine provisioning accepted a template other than the pool default")
	}
	if err := definition.ValidateMachineProvisioning(
		policy,
		testMachineProvisioning(t, testTemplate, 3, 4096),
	); err == nil {
		t.Fatal("validate machine provisioning accepted a size Agent37 does not sell")
	}
	wildcard := testPoolPolicy(t, mustRawJSON(t, map[string]any{"allowed_templates": []string{"*"}}), testTemplate)
	if err := definition.ValidateMachineProvisioning(wildcard, other); err != nil {
		t.Fatalf("validate machine provisioning with a wildcard allowlist: %v", err)
	}
	intent, err := definition.BuildMachineProvisioningIntent(policy, testMachineProvisioning(t, testTemplate, 4, 8192))
	if err != nil || intent.CPU == nil || *intent.CPU != 4 || intent.MemoryMB == nil || *intent.MemoryMB != 8192 {
		t.Fatalf("intent = %+v error = %v, want the configured size kept", intent, err)
	}
}

func TestAgent37ResourcePolicyConfiguresCPUAndMemory(t *testing.T) {
	policy := (Definition{}).ResourcePolicy()
	for name, contract := range map[string]providers.MachineResourceContract{
		"cpu":       policy.CPU,
		"memory_mb": policy.MemoryMB,
	} {
		if contract.PoolDefault != providers.MachineResourceRequired ||
			contract.Limits != providers.MachineResourceRequired ||
			contract.Provisioning != providers.MachineResourceConfigured {
			t.Fatalf("%s contract = %+v, want required defaults and limits, configured provisioning", name, contract)
		}
	}
}

func TestAgent37NewProviderRequiresAnAuthToken(t *testing.T) {
	_, err := (Definition{}).NewProvider(json.RawMessage(`{}`), providers.RuntimeConfig{ProviderAuthToken: " "})
	if err == nil || !strings.Contains(err.Error(), "auth token") {
		t.Fatalf("new provider error = %v, want a missing token to be reported", err)
	}
	if _, err := (Definition{}).NewProvider(json.RawMessage(`{"nope":1}`), providers.RuntimeConfig{ProviderAuthToken: "k"}); err == nil {
		t.Fatal("new provider accepted an invalid provider config")
	}
	runtimeConfig := providers.RuntimeConfig{
		OmnaraAPIURL:      "https://api.omnara.test/v1",
		ProviderAuthToken: " sk_live_key ",
	}
	for _, construct := range []func() (any, error){
		func() (any, error) { return (Definition{}).NewProvider(nil, runtimeConfig) },
		func() (any, error) { return (Definition{}).NewRuntimeProvider(nil, runtimeConfig) },
	} {
		built, err := construct()
		if err != nil {
			t.Fatalf("new provider: %v", err)
		}
		concrete, ok := built.(*provider)
		if !ok {
			t.Fatal("provider has an unexpected implementation")
		}
		client, ok := concrete.api.(*restClient)
		if !ok || client.apiBaseURL != defaultAPIBaseURL || client.execBaseURL != defaultExecBaseURL ||
			client.apiKey != "sk_live_key" {
			t.Fatalf("client = %+v, want the Agent37 API, its exec address and the trimmed key", concrete.api)
		}
	}
}

func TestAgent37ResolveMachineProviderOptionsLayersOverrides(t *testing.T) {
	options := (Definition{}).ResolveMachineProviderOptions(
		map[string]json.RawMessage{"template": mustRawJSON(t, testTemplate), "disk_gb": mustRawJSON(t, 8)},
		map[string]json.RawMessage{"template": mustRawJSON(t, "acme-project")},
		map[string]json.RawMessage{"startup_script": mustRawJSON(t, "echo agent")},
	)
	parsed, err := parseProviderOptions(options)
	if err != nil {
		t.Fatalf("parse resolved options: %v", err)
	}
	if parsed.Template != "acme-project" || parsed.DiskGB != 8 || parsed.StartupScript != "echo agent" {
		t.Fatalf("resolved options = %+v", parsed)
	}
}
