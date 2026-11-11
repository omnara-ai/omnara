package maritime

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

func TestMaritimeDefinitionRequiresAnAPIKey(t *testing.T) {
	if _, err := (Definition{}).NewProvider(
		nil,
		providers.RuntimeConfig{OmnaraAPIURL: "https://api.omnara.test/v1"},
	); err == nil {
		t.Fatal("new provider without an API key succeeded")
	}
	if _, err := (Definition{}).NewProvider(
		json.RawMessage(`{"unknown":true}`),
		providers.RuntimeConfig{ProviderAuthToken: "mk_test"},
	); err == nil {
		t.Fatal("new provider with an unknown config field succeeded")
	}
	if _, err := (Definition{}).NewRuntimeProvider(
		nil,
		providers.RuntimeConfig{ProviderAuthToken: "mk_test"},
	); err != nil {
		t.Fatalf("new runtime provider: %v", err)
	}
}

func TestMaritimeDefinitionValidatesResources(t *testing.T) {
	definition := Definition{}
	policy := testPoolPolicy(t, nil, "")
	if err := definition.ValidatePool(policy); err != nil {
		t.Fatalf("validate pool: %v", err)
	}
	for name, mutate := range map[string]func(cpu, memory *int){
		"zero cpu":            func(cpu, _ *int) { *cpu = 0 },
		"too much cpu":        func(cpu, _ *int) { *cpu = 17 },
		"memory not in GiB":   func(_, memory *int) { *memory = 1536 },
		"too little memory":   func(_, memory *int) { *memory = 512 },
		"too much memory":     func(_, memory *int) { *memory = 65536 + 1024 },
		"memory not positive": func(_, memory *int) { *memory = -1024 },
	} {
		config := testMachineProvisioning(t, "", "")
		mutate(config.CPU, config.MemoryMB)
		if err := definition.ValidateMachineProvisioning(policy, config); err == nil {
			t.Errorf("%s: validation succeeded", name)
		}
	}
	config := testMachineProvisioning(t, "", "")
	config.CPU = nil
	if err := definition.ValidateMachineProvisioning(policy, config); err == nil {
		t.Error("validation without cpu succeeded")
	}
}

func TestMaritimeDefinitionValidatesOptions(t *testing.T) {
	definition := Definition{}
	policy := testPoolPolicy(t, nil, "")
	for name, options := range map[string]map[string]json.RawMessage{
		"empty template":      {"template": json.RawMessage(`"  "`)},
		"template with space": {"template": json.RawMessage(`"my template"`)},
		"small disk":          {"disk_gb": json.RawMessage(`4`)},
		"large disk":          {"disk_gb": json.RawMessage(`501`)},
		"unknown option":      {"image": json.RawMessage(`"ubuntu"`)},
		"long script":         {"startup_script": mustRawJSON(t, strings.Repeat("x", 64*1024+1))},
	} {
		config := testMachineProvisioning(t, "", "")
		config.ProviderOptions = options
		if err := definition.ValidateMachineProvisioning(policy, config); err == nil {
			t.Errorf("%s: validation succeeded", name)
		}
	}
	config := testMachineProvisioning(t, "", "")
	config.ProviderOptions["disk_gb"] = json.RawMessage(`20`)
	if err := definition.ValidateMachineProvisioning(policy, config); err != nil {
		t.Fatalf("validate disk_gb: %v", err)
	}
}

func TestMaritimeDefinitionAllowsOnlyListedTemplates(t *testing.T) {
	definition := Definition{}
	if err := definition.ValidateMachineProvisioning(
		testPoolPolicy(t, nil, ""), testMachineProvisioning(t, "other", ""),
	); err == nil {
		t.Fatal("a template other than the default was allowed without allowed_templates")
	}
	if err := definition.ValidateMachineProvisioning(
		testPoolPolicy(t, nil, "team-image"), testMachineProvisioning(t, "team-image", ""),
	); err != nil {
		t.Fatalf("the pool's default template was refused: %v", err)
	}
	allowed := mustRawJSON(t, map[string]any{"allowed_templates": []string{"base", "ghcr.io/acme/agent:1.2"}})
	if err := definition.ValidateMachineProvisioning(
		testPoolPolicy(t, allowed, ""), testMachineProvisioning(t, "ghcr.io/acme/agent:1.2", ""),
	); err != nil {
		t.Fatalf("an allowed image was refused: %v", err)
	}
	if err := definition.ValidateMachineProvisioning(
		testPoolPolicy(t, allowed, ""), testMachineProvisioning(t, "code-interpreter", ""),
	); err == nil {
		t.Fatal("a template outside allowed_templates was allowed")
	}
	wildcard := mustRawJSON(t, map[string]any{"allowed_templates": []string{"*"}})
	if err := definition.ValidateMachineProvisioning(
		testPoolPolicy(t, wildcard, ""), testMachineProvisioning(t, "code-interpreter", ""),
	); err != nil {
		t.Fatalf("the wildcard refused a template: %v", err)
	}
}

func TestMaritimeBuildMachineProvisioningIntentKeepsResources(t *testing.T) {
	config := testMachineProvisioning(t, "", "")
	intent, err := (Definition{}).BuildMachineProvisioningIntent(testPoolPolicy(t, nil, ""), config)
	if err != nil {
		t.Fatalf("build intent: %v", err)
	}
	if intent.CPU == nil || *intent.CPU != 2 || intent.MemoryMB == nil || *intent.MemoryMB != 4096 {
		t.Fatalf("intent = %+v, want the configured resources", intent)
	}
}
