package createos

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestCreateOSProviderConfigDefaultsToThePublicAPI(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, json.RawMessage("null"), json.RawMessage(`{}`)} {
		config, err := parseProviderConfig(raw)
		if err != nil {
			t.Fatalf("parse provider config %s: %v", raw, err)
		}
		if config.APIBaseURL != defaultAPIBaseURL {
			t.Fatalf("api base url = %q, want %q", config.APIBaseURL, defaultAPIBaseURL)
		}
	}
}

func TestCreateOSProviderConfigNormalizesBaseURL(t *testing.T) {
	config, err := parseProviderConfig(json.RawMessage(`{"api_base_url":"https://api.example.test/"}`))
	if err != nil {
		t.Fatalf("parse provider config: %v", err)
	}
	if config.APIBaseURL != "https://api.example.test" {
		t.Fatalf("api base url = %q, want the trailing slash removed", config.APIBaseURL)
	}
}

func TestCreateOSProviderConfigRejectsUnsafeBaseURLs(t *testing.T) {
	for name, raw := range map[string]string{
		"plain http":  `{"api_base_url":"http://api.example.test"}`,
		"no scheme":   `{"api_base_url":"api.example.test"}`,
		"query":       `{"api_base_url":"https://api.example.test?token=1"}`,
		"fragment":    `{"api_base_url":"https://api.example.test#section"}`,
		"unknown key": `{"api_base_urls":"https://api.example.test"}`,
	} {
		if _, err := parseProviderConfig(json.RawMessage(raw)); err == nil {
			t.Errorf("%s: parse provider config accepted %s", name, raw)
		}
	}
}

func TestCreateOSProviderOptionsRequireAShape(t *testing.T) {
	if _, err := parseProviderOptions(nil); err == nil {
		t.Fatal("parse provider options accepted a machine config with no provider options")
	}
	for name, options := range map[string]map[string]json.RawMessage{
		"no shape":        testOptions(t, "", "devbox:1", "us", ""),
		"invalid shape":   testOptions(t, "Not A Shape", "devbox:1", "us", ""),
		"wildcard rootfs": testOptions(t, "s-1vcpu-1gb", "*", "us", ""),
	} {
		if _, err := parseProviderOptions(options); err == nil {
			t.Errorf("%s: parse provider options accepted %v", name, options)
		}
	}
}

func TestCreateOSProviderOptionsTrimValues(t *testing.T) {
	options, err := parseProviderOptions(testOptions(t, " s-1vcpu-1gb ", " devbox:1 ", " us ", ""))
	if err != nil {
		t.Fatalf("parse provider options: %v", err)
	}
	if options.Shape != "s-1vcpu-1gb" || options.RootFS != "devbox:1" {
		t.Fatalf("provider options = %+v, want trimmed values", options)
	}
}

func TestCreateOSProviderOptionsRequireARootFS(t *testing.T) {
	if _, err := parseProviderOptions(testOptions(t, "s-1vcpu-1gb", "", "us", "")); err == nil {
		t.Fatal("parse provider options accepted a machine config with no rootfs")
	}
}

func TestCreateOSProviderOptionsIgnoreLegacyRegion(t *testing.T) {
	if _, err := parseProviderOptions(testOptions(t, "s-1vcpu-1gb", "devbox:1", "Not A Region", "")); err != nil {
		t.Fatalf("parse provider options: %v", err)
	}
}

func TestCreateOSValidatePoolEnforcesAllowlists(t *testing.T) {
	config := mustRawJSON(t, map[string]any{
		"allowed_shapes":   []string{"s-1vcpu-1gb"},
		"allowed_rootfses": []string{"devbox:1"},
		"allowed_regions":  []string{"legacy-value"},
	})
	definition := Definition{}
	if err := definition.ValidatePool(testPoolPolicy(t, config, "s-1vcpu-1gb", "devbox:1", "us")); err != nil {
		t.Fatalf("validate pool: %v", err)
	}
	for name, policy := range map[string]executionstore.MachinePoolProviderPolicy{
		"shape":  testPoolPolicy(t, config, "s-4vcpu-8gb", "devbox:1", "us"),
		"rootfs": testPoolPolicy(t, config, "s-1vcpu-1gb", "ubuntu:24.04", "us"),
	} {
		if err := definition.ValidatePool(policy); err == nil {
			t.Errorf("validate pool accepted a %s outside the allowlist", name)
		}
	}
}

func TestCreateOSValidateMachineProvisioningEnforcesPoolAllowlists(t *testing.T) {
	config := mustRawJSON(t, map[string]any{"allowed_shapes": []string{"s-1vcpu-1gb"}})
	policy := testPoolPolicy(t, config, "s-1vcpu-1gb", "devbox:1", "us")
	definition := Definition{}
	if err := definition.ValidateMachineProvisioning(
		policy,
		testMachineProvisioning(t, "s-1vcpu-1gb", "devbox:1", "us", ""),
	); err != nil {
		t.Fatalf("validate machine provisioning: %v", err)
	}
	if err := definition.ValidateMachineProvisioning(
		policy,
		testMachineProvisioning(t, "s-4vcpu-8gb", "devbox:1", "us", ""),
	); err == nil {
		t.Fatal("validate machine provisioning accepted a shape outside the pool allowlist")
	}
}

func TestCreateOSBuildMachineProvisioningIntentLeavesResourcesToTheProvider(t *testing.T) {
	policy := testPoolPolicy(t, json.RawMessage(`{}`), "s-1vcpu-1gb", "devbox:1", "us")
	provisioning := testMachineProvisioning(t, "s-1vcpu-1gb", "devbox:1", "us", "")
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
	if _, ok := intent.ProviderOptions["region"]; ok {
		t.Fatalf("intent provider options = %v, want legacy region removed", intent.ProviderOptions)
	}
}

func TestCreateOSBuildMachineProvisioningIntentRejectsInvalidInput(t *testing.T) {
	policy := testPoolPolicy(t, json.RawMessage(`{}`), "s-1vcpu-1gb", "devbox:1", "us")
	if _, err := (Definition{}).BuildMachineProvisioningIntent(
		policy,
		testMachineProvisioning(t, "", "devbox:1", "us", ""),
	); err == nil {
		t.Fatal("build machine provisioning intent accepted an empty shape")
	}
}

func TestCreateOSResourcePolicyLetsTheShapeDecide(t *testing.T) {
	policy := (Definition{}).ResourcePolicy()
	if policy.CPU.Provisioning != providers.MachineResourceProviderResolved {
		t.Fatalf("cpu provisioning = %v, want provider resolved", policy.CPU.Provisioning)
	}
	if policy.MemoryMB.Provisioning != providers.MachineResourceProviderResolved {
		t.Fatalf("memory provisioning = %v, want provider resolved", policy.MemoryMB.Provisioning)
	}
}

func TestCreateOSNewProviderRequiresAnAuthToken(t *testing.T) {
	_, err := (Definition{}).NewProvider(json.RawMessage(`{}`), providers.RuntimeConfig{})
	if err == nil || !strings.Contains(err.Error(), "auth token") {
		t.Fatalf("new provider error = %v, want a missing token to be reported", err)
	}
}

func TestCreateOSNewProviderUsesConfiguredBaseURL(t *testing.T) {
	machineProvider, err := (Definition{}).NewProvider(
		json.RawMessage(`{"api_base_url":"https://api.example.test"}`),
		providers.RuntimeConfig{OmnaraAPIURL: "https://api.omnara.test/v1", ProviderAuthToken: "token"},
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
	if client.baseURL != "https://api.example.test" {
		t.Fatalf("client base url = %q, want the configured one", client.baseURL)
	}
}

func TestCreateOSNewRuntimeProviderObservesRuntimeState(t *testing.T) {
	runtimeProvider, err := (Definition{}).NewRuntimeProvider(
		json.RawMessage(`{}`),
		providers.RuntimeConfig{ProviderAuthToken: "token"},
	)
	if err != nil {
		t.Fatalf("new runtime provider: %v", err)
	}
	if _, ok := runtimeProvider.(providers.RuntimeStateObserver); !ok {
		t.Fatal("runtime provider does not observe runtime state")
	}
}

func TestCreateOSResolveMachineProviderOptionsLayersOverrides(t *testing.T) {
	options := (Definition{}).ResolveMachineProviderOptions(
		map[string]json.RawMessage{"shape": mustRawJSON(t, "s-1vcpu-1gb"), "region": mustRawJSON(t, "us")},
		map[string]json.RawMessage{"shape": mustRawJSON(t, "s-2vcpu-4gb")},
		map[string]json.RawMessage{"rootfs": mustRawJSON(t, "devbox:1")},
	)
	var shape, rootfs string
	if err := json.Unmarshal(options["shape"], &shape); err != nil {
		t.Fatalf("decode shape: %v", err)
	}
	if _, ok := options["region"]; ok {
		t.Fatalf("resolved options = %v, want legacy region removed", options)
	}
	if err := json.Unmarshal(options["rootfs"], &rootfs); err != nil {
		t.Fatalf("decode rootfs: %v", err)
	}
	if shape != "s-2vcpu-4gb" || rootfs != "devbox:1" {
		t.Fatalf("resolved options = shape %q rootfs %q", shape, rootfs)
	}
}
