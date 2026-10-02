package createos

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestCreateOSProviderConfigAcceptsEmptyConfig(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, json.RawMessage("null"), json.RawMessage(`{}`)} {
		config, err := parseProviderConfig(raw)
		if err != nil {
			t.Fatalf("parse provider config %s: %v", raw, err)
		}
		if config.AllowedShapes != nil || config.AllowedRootFSes != nil {
			t.Fatalf("provider config = %+v, want no allowlists", config)
		}
	}
}

func TestCreateOSProviderConfigRejectsUnknownAndInvalidFields(t *testing.T) {
	for name, raw := range map[string]string{
		"api base url":       `{"api_base_url":"https://api.example.test"}`,
		"regions":            `{"allowed_regions":["us"]}`,
		"empty shapes":       `{"allowed_shapes":[]}`,
		"mixed wildcard":     `{"allowed_shapes":["*","s-1vcpu-1gb"]}`,
		"empty rootfs entry": `{"allowed_rootfses":[" "]}`,
	} {
		if _, err := parseProviderConfig(json.RawMessage(raw)); err == nil {
			t.Errorf("%s: parse provider config accepted %s", name, raw)
		}
	}
}

func TestCreateOSProviderOptionsRequireShapeAndRootFS(t *testing.T) {
	if _, err := parseProviderOptions(nil); err == nil {
		t.Fatal("parse provider options accepted a machine config with no provider options")
	}
	for name, options := range map[string]map[string]json.RawMessage{
		"no shape":        testOptions(t, "", "devbox:1", ""),
		"wildcard shape":  testOptions(t, "*", "devbox:1", ""),
		"no rootfs":       testOptions(t, "s-1vcpu-1gb", "", ""),
		"wildcard rootfs": testOptions(t, "s-1vcpu-1gb", "*", ""),
	} {
		if _, err := parseProviderOptions(options); err == nil {
			t.Errorf("%s: parse provider options accepted %v", name, options)
		}
	}
}

func TestCreateOSProviderOptionsRejectUnknownKeys(t *testing.T) {
	options := testOptions(t, "s-1vcpu-1gb", "devbox:1", "")
	options["region"] = mustRawJSON(t, "us")
	if _, err := parseProviderOptions(options); err == nil || !strings.Contains(err.Error(), "region") {
		t.Fatalf("parse provider options error = %v, want region rejected", err)
	}
}

func TestCreateOSProviderOptionsAcceptFractionalShapesAndTrim(t *testing.T) {
	options, err := parseProviderOptions(testOptions(t, " s-0.25vcpu-512mb ", " devbox:1 ", ""))
	if err != nil {
		t.Fatalf("parse provider options: %v", err)
	}
	if options.Shape != "s-0.25vcpu-512mb" || options.RootFS != "devbox:1" {
		t.Fatalf("provider options = %+v, want trimmed values", options)
	}
}

func TestCreateOSProviderOptionsBoundStartupScriptToOneEnvValue(t *testing.T) {
	if _, err := parseProviderOptions(
		testOptions(t, "s-1vcpu-1gb", "devbox:1", strings.Repeat("a", 3072)),
	); err != nil {
		t.Fatalf("parse provider options with the largest startup script: %v", err)
	}
	_, err := parseProviderOptions(testOptions(t, "s-1vcpu-1gb", "devbox:1", strings.Repeat("a", 3073)))
	if err == nil || !strings.Contains(err.Error(), "at most 3072 bytes") {
		t.Fatalf("parse provider options error = %v, want the startup script limit", err)
	}
}

func TestCreateOSValidatePoolEnforcesAllowlists(t *testing.T) {
	config := mustRawJSON(t, map[string]any{
		"allowed_shapes":   []string{"s-1vcpu-1gb"},
		"allowed_rootfses": []string{"devbox:1"},
	})
	definition := Definition{}
	if err := definition.ValidatePool(testPoolPolicy(t, config, "s-1vcpu-1gb", "devbox:1")); err != nil {
		t.Fatalf("validate pool: %v", err)
	}
	for name, policy := range map[string]executionstore.MachinePoolProviderPolicy{
		"shape":  testPoolPolicy(t, config, "s-4vcpu-8gb", "devbox:1"),
		"rootfs": testPoolPolicy(t, config, "s-1vcpu-1gb", "ubuntu:26.04"),
	} {
		if err := definition.ValidatePool(policy); err == nil {
			t.Errorf("validate pool accepted a %s outside the allowlist", name)
		}
	}
}

func TestCreateOSValidateMachineProvisioningDefaultsAllowlistsToPoolOptions(t *testing.T) {
	policy := testPoolPolicy(t, json.RawMessage(`{}`), "s-1vcpu-1gb", "devbox:1")
	definition := Definition{}
	if err := definition.ValidateMachineProvisioning(
		policy,
		testMachineProvisioning(t, "s-1vcpu-1gb", "devbox:1", "echo ready"),
	); err != nil {
		t.Fatalf("validate machine provisioning: %v", err)
	}
	for name, provisioning := range map[string]executionstore.MachineProvisioningConfig{
		"shape":  testMachineProvisioning(t, "s-4vcpu-8gb", "devbox:1", ""),
		"rootfs": testMachineProvisioning(t, "s-1vcpu-1gb", "ubuntu:26.04", ""),
	} {
		if err := definition.ValidateMachineProvisioning(policy, provisioning); err == nil {
			t.Errorf("validate machine provisioning accepted a %s other than the pool default", name)
		}
	}
	wildcard := testPoolPolicy(
		t,
		mustRawJSON(t, map[string]any{"allowed_shapes": []string{"*"}, "allowed_rootfses": []string{"*"}}),
		"s-1vcpu-1gb",
		"devbox:1",
	)
	if err := definition.ValidateMachineProvisioning(
		wildcard,
		testMachineProvisioning(t, "s-4vcpu-8gb", "ubuntu:26.04", ""),
	); err != nil {
		t.Fatalf("validate machine provisioning with wildcard allowlists: %v", err)
	}
}

func TestCreateOSBuildMachineProvisioningIntentLeavesResourcesToTheShape(t *testing.T) {
	policy := testPoolPolicy(t, json.RawMessage(`{}`), "s-1vcpu-1gb", "devbox:1")
	provisioning := testMachineProvisioning(t, "s-1vcpu-1gb", "devbox:1", "")
	cpu := 4
	memoryMB := 8192
	provisioning.CPU = &cpu
	provisioning.MemoryMB = &memoryMB
	intent, err := (Definition{}).BuildMachineProvisioningIntent(policy, provisioning)
	if err != nil {
		t.Fatalf("build machine provisioning intent: %v", err)
	}
	if intent.CPU != nil || intent.MemoryMB != nil {
		t.Fatalf("intent = %+v, want the shape to decide cpu and memory", intent)
	}
	if _, err := (Definition{}).BuildMachineProvisioningIntent(
		policy,
		testMachineProvisioning(t, "", "devbox:1", ""),
	); err == nil {
		t.Fatal("build machine provisioning intent accepted an empty shape")
	}
}

func TestCreateOSResourcePolicyLetsTheShapeDecide(t *testing.T) {
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

func TestCreateOSNewProviderRequiresAnAuthToken(t *testing.T) {
	_, err := (Definition{}).NewProvider(json.RawMessage(`{}`), providers.RuntimeConfig{ProviderAuthToken: " "})
	if err == nil || !strings.Contains(err.Error(), "auth token") {
		t.Fatalf("new provider error = %v, want a missing token to be reported", err)
	}
}

func TestCreateOSNewProviderUsesThePublicAPI(t *testing.T) {
	machineProvider, err := (Definition{}).NewProvider(
		json.RawMessage(`{}`),
		providers.RuntimeConfig{OmnaraAPIURL: "https://api.omnara.test/v1", ProviderAuthToken: " token "},
	)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	concrete, ok := machineProvider.(*provider)
	if !ok {
		t.Fatal("provider has an unexpected implementation")
	}
	client, ok := concrete.api.(*restClient)
	if !ok {
		t.Fatal("provider does not use the REST client")
	}
	if client.baseURL != apiBaseURL || client.token != "token" {
		t.Fatalf("client = %q with token %q, want the public API and the trimmed token", client.baseURL, client.token)
	}
}

func TestCreateOSResolveMachineProviderOptionsLayersOverrides(t *testing.T) {
	options := (Definition{}).ResolveMachineProviderOptions(
		map[string]json.RawMessage{"shape": mustRawJSON(t, "s-1vcpu-1gb"), "rootfs": mustRawJSON(t, "devbox:1")},
		map[string]json.RawMessage{"shape": mustRawJSON(t, "s-2vcpu-4gb")},
		map[string]json.RawMessage{"rootfs": mustRawJSON(t, "ubuntu:26.04")},
	)
	var shape, rootfs string
	if err := json.Unmarshal(options["shape"], &shape); err != nil {
		t.Fatalf("decode shape: %v", err)
	}
	if err := json.Unmarshal(options["rootfs"], &rootfs); err != nil {
		t.Fatalf("decode rootfs: %v", err)
	}
	if shape != "s-2vcpu-4gb" || rootfs != "ubuntu:26.04" {
		t.Fatalf("resolved options = shape %q rootfs %q", shape, rootfs)
	}
}
