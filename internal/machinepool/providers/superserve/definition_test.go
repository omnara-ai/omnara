package superserve

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestSuperserveProviderConfigAcceptsEmptyConfig(t *testing.T) {
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

func TestSuperserveProviderConfigRejectsUnknownAndInvalidFields(t *testing.T) {
	for name, raw := range map[string]string{
		"api base url":   `{"api_base_url":"https://api.example.test"}`,
		"regions":        `{"allowed_regions":["use"]}`,
		"empty list":     `{"allowed_templates":[]}`,
		"mixed wildcard": `{"allowed_templates":["*","superserve/base"]}`,
		"empty entry":    `{"allowed_templates":[" "]}`,
	} {
		if _, err := parseProviderConfig(json.RawMessage(raw)); err == nil {
			t.Errorf("%s: parse provider config accepted %s", name, raw)
		}
	}
}

func TestSuperserveProviderOptionsDefaultToTheOmnaraTemplate(t *testing.T) {
	for name, raw := range map[string]map[string]json.RawMessage{
		"nil":   nil,
		"empty": {},
	} {
		options, err := parseProviderOptions(raw)
		if err != nil {
			t.Fatalf("%s: parse provider options: %v", name, err)
		}
		if options.Template != "" || options.template() != defaultTemplate {
			t.Fatalf("%s: provider options = %+v, want superserve/omnara by default", name, options)
		}
	}
	options, err := parseProviderOptions(testOptions(t, " acme/agent-box ", "echo ready"))
	if err != nil {
		t.Fatalf("parse provider options: %v", err)
	}
	if options.template() != "acme/agent-box" || options.StartupScript != "echo ready" {
		t.Fatalf("provider options = %+v, want the trimmed template", options)
	}
}

func TestSuperserveProviderOptionsRejectInvalidValues(t *testing.T) {
	for name, raw := range map[string]map[string]json.RawMessage{
		"empty template":    {"template": json.RawMessage(`""`)},
		"blank template":    {"template": json.RawMessage(`"  "`)},
		"null template":     {"template": json.RawMessage(`null`)},
		"wildcard template": {"template": json.RawMessage(`"*"`)},
		"unknown key":       {"image": json.RawMessage(`"ubuntu"`)},
		"numeric template":  {"template": json.RawMessage(`1`)},
		"newline template":  {"template": json.RawMessage(`"a\nb"`)},
		"long script":       testOptions(t, "", strings.Repeat("a", 64*1024+1)),
	} {
		if _, err := parseProviderOptions(raw); err == nil {
			t.Errorf("%s: parse provider options accepted %v", name, raw)
		}
	}
}

func TestSuperserveValidatePoolEnforcesTheTemplateAllowlist(t *testing.T) {
	definition := Definition{}
	config := mustRawJSON(t, map[string]any{"allowed_templates": []string{"acme/agent-box"}})
	if err := definition.ValidatePool(testPoolPolicy(t, config, "acme/agent-box")); err != nil {
		t.Fatalf("validate pool: %v", err)
	}
	if err := definition.ValidatePool(testPoolPolicy(t, config, "")); err == nil {
		t.Fatal("validate pool accepted the default template outside the allowlist")
	}
	if err := definition.ValidatePool(testPoolPolicy(t, json.RawMessage(`{}`), "")); err != nil {
		t.Fatalf("validate pool without an allowlist: %v", err)
	}
	policy := testPoolPolicy(t, json.RawMessage(`{}`), "")
	policy.ResourceLimits.MaxMachineCPU = nil
	if err := definition.ValidatePool(policy); err == nil {
		t.Fatal("validate pool accepted a missing cpu limit")
	}
}

func TestSuperserveValidateMachineProvisioningDefaultsAllowlistToPoolTemplate(t *testing.T) {
	definition := Definition{}
	policy := testPoolPolicy(t, json.RawMessage(`{}`), "")
	for name, provisioning := range map[string]executionstore.MachineProvisioningConfig{
		"implicit default": testMachineProvisioning(t, "", "echo ready"),
		"explicit default": testMachineProvisioning(t, defaultTemplate, ""),
	} {
		if err := definition.ValidateMachineProvisioning(policy, provisioning); err != nil {
			t.Fatalf("%s: validate machine provisioning: %v", name, err)
		}
	}
	if err := definition.ValidateMachineProvisioning(policy, testMachineProvisioning(t, "acme/other", "")); err == nil {
		t.Fatal("validate machine provisioning accepted a template other than the pool default")
	}
	wildcard := testPoolPolicy(t, mustRawJSON(t, map[string]any{"allowed_templates": []string{"*"}}), "")
	if err := definition.ValidateMachineProvisioning(wildcard, testMachineProvisioning(t, "acme/other", "")); err != nil {
		t.Fatalf("validate machine provisioning with a wildcard allowlist: %v", err)
	}
	cpu := 1
	sized := testMachineProvisioning(t, "", "")
	sized.CPU = &cpu
	if err := definition.ValidateMachineProvisioning(policy, sized); err != nil {
		t.Fatalf("validate machine provisioning with resolved cpu: %v", err)
	}
	zero := 0
	sized.CPU = &zero
	if err := definition.ValidateMachineProvisioning(policy, sized); err == nil {
		t.Fatal("validate machine provisioning accepted a zero cpu")
	}
}

func TestSuperserveBuildMachineProvisioningIntentLeavesResourcesToTheTemplate(t *testing.T) {
	policy := testPoolPolicy(t, json.RawMessage(`{}`), "")
	provisioning := testMachineProvisioning(t, "", "")
	cpu := 4
	memoryMB := 8192
	provisioning.CPU = &cpu
	provisioning.MemoryMB = &memoryMB
	intent, err := (Definition{}).BuildMachineProvisioningIntent(policy, provisioning)
	if err != nil {
		t.Fatalf("build machine provisioning intent: %v", err)
	}
	if intent.CPU != nil || intent.MemoryMB != nil {
		t.Fatalf("intent = %+v, want the template to decide cpu and memory", intent)
	}
	if _, err := (Definition{}).BuildMachineProvisioningIntent(
		policy,
		executionstore.MachineProvisioningConfig{ProviderOptions: map[string]json.RawMessage{"template": json.RawMessage(`""`)}},
	); err == nil {
		t.Fatal("build machine provisioning intent accepted an empty template")
	}
}

func TestSuperserveResourcePolicyLetsTheTemplateDecide(t *testing.T) {
	policy := (Definition{}).ResourcePolicy()
	for name, contract := range map[string]providers.MachineResourceContract{
		"cpu":       policy.CPU,
		"memory_mb": policy.MemoryMB,
	} {
		if contract.PoolDefault != providers.MachineResourceOptional ||
			contract.Limits != providers.MachineResourceRequired ||
			contract.Provisioning != providers.MachineResourceProviderResolved {
			t.Fatalf("%s contract = %+v, want optional default, required limits, provider resolved", name, contract)
		}
	}
}

func TestSuperserveNewProviderRequiresAnAuthToken(t *testing.T) {
	_, err := (Definition{}).NewProvider(json.RawMessage(`{}`), providers.RuntimeConfig{ProviderAuthToken: " "})
	if err == nil || !strings.Contains(err.Error(), "auth token") {
		t.Fatalf("new provider error = %v, want a missing token to be reported", err)
	}
	if _, err := (Definition{}).NewProvider(json.RawMessage(`{"nope":1}`), providers.RuntimeConfig{ProviderAuthToken: "k"}); err == nil {
		t.Fatal("new provider accepted an invalid provider config")
	}
}

func TestSuperserveNewProviderPicksTheRegionFromTheKey(t *testing.T) {
	key := " ss_live_usw_" + strings.Repeat("b", 32) + " "
	runtimeConfig := providers.RuntimeConfig{OmnaraAPIURL: "https://api.omnara.test/v1", ProviderAuthToken: key}
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
		if !ok {
			t.Fatal("provider does not use the REST client")
		}
		if client.apiBaseURL != knownRegions["usw"].apiBaseURL || client.sandboxHost != knownRegions["usw"].sandboxHost ||
			client.apiKey != strings.TrimSpace(key) {
			t.Fatalf("client = %+v, want the usw endpoints and the trimmed key", client)
		}
		if concrete.omnaraAPIURL != "https://api.omnara.test/v1" || concrete.pollDelay != pollDelay {
			t.Fatalf("provider = %+v", concrete)
		}
	}
}

func TestSuperserveResolveMachineProviderOptionsLayersOverrides(t *testing.T) {
	options := (Definition{}).ResolveMachineProviderOptions(
		map[string]json.RawMessage{"template": mustRawJSON(t, "superserve/base")},
		map[string]json.RawMessage{"template": mustRawJSON(t, "acme/project")},
		map[string]json.RawMessage{"startup_script": mustRawJSON(t, "echo agent")},
	)
	parsed, err := parseProviderOptions(options)
	if err != nil {
		t.Fatalf("parse resolved options: %v", err)
	}
	if parsed.Template != "acme/project" || parsed.StartupScript != "echo agent" {
		t.Fatalf("resolved options = %+v", parsed)
	}
}
