package unikraft

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestDefinitionCreatesProviderFromConfig(t *testing.T) {
	runtime, err := (Definition{}).NewProvider(
		json.RawMessage(`{"allowed_images":["registry.example/daemon:latest"],"allowed_metros":["sfo"]}`),
		providers.RuntimeConfig{
			PublicURL:         "https://app.omnara.test",
			ProviderAuthToken: "token",
		},
	)
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	provider, ok := runtime.(*provider)
	if !ok {
		t.Fatalf("provider type = %T, want *provider", runtime)
	}
	if provider.apiBaseURL != "" {
		t.Fatalf("api base url = %q, want empty so metro derives hosted endpoint", provider.apiBaseURL)
	}
	if provider.omnaraPublicURL != "https://app.omnara.test" {
		t.Fatalf("omnara public url = %q", provider.omnaraPublicURL)
	}
}

func TestDefinitionCreatesProviderWithCustomBaseURL(t *testing.T) {
	runtime, err := (Definition{}).NewProvider(
		json.RawMessage(
			`{"api_base_url":"https://api.custom.example/","allowed_images":["registry.example/daemon:latest"],"allowed_metros":["sfo"]}`,
		),
		providers.RuntimeConfig{PublicURL: "https://app.omnara.test", ProviderAuthToken: "token"},
	)
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	provider, ok := runtime.(*provider)
	if !ok {
		t.Fatalf("provider type = %T, want *provider", runtime)
	}
	if provider.apiToken != "token" || provider.apiBaseURL != "https://api.custom.example" {
		t.Fatalf("provider config apiToken=%q apiBaseURL=%q", provider.apiToken, provider.apiBaseURL)
	}
}

func TestValidateProviderConfigDoesNotRequireUnikraftToken(t *testing.T) {
	if err := validateProvisioningForTest(
		validProvisioning(),
		validProvisioning(),
		json.RawMessage(`{}`),
	); err != nil {
		t.Fatalf("validate pool without auth token: %v", err)
	}
}

func TestValidateProviderConfigRejectsInvalidUnikraftBaseURL(t *testing.T) {
	if err := validateProvisioningForTest(
		validProvisioning(),
		validProvisioning(),
		json.RawMessage(`{"api_base_url":"not-a-url"}`),
	); err == nil {
		t.Fatal("expected invalid unikraft api base url to fail")
	}
}

func TestValidateProviderConfigRejectsUnikraftUnknownFields(t *testing.T) {
	if err := validateProvisioningForTest(
		validProvisioning(),
		validProvisioning(),
		json.RawMessage(`{"extra":true}`),
	); err == nil {
		t.Fatal("expected unknown unikraft provider config field to fail")
	}
}

func TestValidateProviderConfigRejectsInvalidUnikraftMachineConfig(t *testing.T) {
	machineProvisioning := validProvisioning()
	delete(machineProvisioning.ProviderOptions, "image")
	err := validateProvisioningForTest(
		machineProvisioning,
		machineProvisioning,
		json.RawMessage(`{}`),
	)
	if err == nil {
		t.Fatal("expected missing unikraft image to fail")
	}
}

func TestValidateProviderConfigRejectsWildcardImage(t *testing.T) {
	machineProvisioning := validProvisioning()
	machineProvisioning.ProviderOptions["image"] = json.RawMessage(`"*"`)
	err := validateProvisioningForTest(
		machineProvisioning,
		machineProvisioning,
		json.RawMessage(`{}`),
	)
	if err == nil || !strings.Contains(err.Error(), `must not be "*"`) {
		t.Fatalf("wildcard image error = %v, want rejection", err)
	}
}

func TestValidatePoolRejectsUnikraftUnknownProviderOptions(t *testing.T) {
	machineProvisioning := validProvisioning()
	machineProvisioning.ProviderOptions["extra"] = json.RawMessage(`true`)
	err := validateProvisioningForTest(
		machineProvisioning,
		machineProvisioning,
		json.RawMessage(`{}`),
	)
	if err == nil {
		t.Fatal("expected unknown unikraft config field to fail")
	}
}

func TestValidateProviderConfigAllowsWildcardAllowlists(t *testing.T) {
	err := validateProvisioningForTest(
		validProvisioning(),
		validProvisioning(),
		json.RawMessage(`{"allowed_images":["*"],"allowed_metros":["*"]}`),
	)
	if err != nil {
		t.Fatalf("validate wildcard provider config: %v", err)
	}
}

func TestValidateProviderConfigRejectsInvalidAllowlists(t *testing.T) {
	for _, tc := range []struct {
		name           string
		providerConfig json.RawMessage
		wantErr        string
	}{
		{
			name:           "empty images",
			providerConfig: json.RawMessage(`{"allowed_images":[],"allowed_metros":["sfo"]}`),
			wantErr:        "allowed_images must not be empty",
		},
		{
			name:           "empty metros",
			providerConfig: json.RawMessage(`{"allowed_images":["registry.example/daemon:latest"],"allowed_metros":[]}`),
			wantErr:        "allowed_metros must not be empty",
		},
		{
			name:           "mixed image wildcard",
			providerConfig: json.RawMessage(`{"allowed_images":["*","registry.example/daemon:latest"],"allowed_metros":["sfo"]}`),
			wantErr:        "allowed_images cannot mix wildcard",
		},
		{
			name:           "mixed metro wildcard",
			providerConfig: json.RawMessage(`{"allowed_images":["registry.example/daemon:latest"],"allowed_metros":["*","sfo"]}`),
			wantErr:        "allowed_metros cannot mix wildcard",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateProvisioningForTest(
				validProvisioning(),
				validProvisioning(),
				tc.providerConfig,
			)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validate provider config error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidateProviderConfigDefaultsOmittedAllowlistsToPoolDefault(t *testing.T) {
	poolDefault := validProvisioning()
	if err := validateProvisioningForTest(
		poolDefault,
		poolDefault,
		json.RawMessage(`{}`),
	); err != nil {
		t.Fatalf("validate default-only provider config: %v", err)
	}

	machineProvisioning := validProvisioning()
	machineProvisioning.ProviderOptions["image"] = json.RawMessage(`"registry.example/other:latest"`)
	err := validateProvisioningForTest(poolDefault, machineProvisioning, json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "provider_config.allowed_images") {
		t.Fatalf("default-only disallowed image error = %v, want allowed_images", err)
	}

	machineProvisioning = validProvisioning()
	machineProvisioning.ProviderOptions["metro"] = json.RawMessage(`"iad"`)
	err = validateProvisioningForTest(poolDefault, machineProvisioning, json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "provider_config.allowed_metros") {
		t.Fatalf("default-only disallowed metro error = %v, want allowed_metros", err)
	}
}

func TestValidatePoolRejectsUnikraftDisallowedImageAndMetro(t *testing.T) {
	machineProvisioning := validProvisioning()
	machineProvisioning.ProviderOptions["image"] = json.RawMessage(`"registry.example/other:latest"`)
	err := validateProvisioningForTest(
		validProvisioning(),
		machineProvisioning,
		json.RawMessage(
			`{"allowed_images":["registry.example/daemon:latest"],"allowed_metros":["sfo"]}`,
		),
	)
	if err == nil || !strings.Contains(err.Error(), "provider_config.allowed_images") {
		t.Fatalf("disallowed image error = %v, want allowed_images", err)
	}

	machineProvisioning = validProvisioning()
	machineProvisioning.ProviderOptions["metro"] = json.RawMessage(`"iad"`)
	err = validateProvisioningForTest(
		validProvisioning(),
		machineProvisioning,
		json.RawMessage(
			`{"allowed_images":["registry.example/daemon:latest"],"allowed_metros":["sfo"]}`,
		),
	)
	if err == nil || !strings.Contains(err.Error(), "provider_config.allowed_metros") {
		t.Fatalf("disallowed metro error = %v, want allowed_metros", err)
	}
}

func validProvisioning() executionstore.MachineProvisioningConfig {
	cpu := 1
	memoryMB := 1024
	return executionstore.MachineProvisioningConfig{
		CPU:      &cpu,
		MemoryMB: &memoryMB,
		ProviderOptions: map[string]json.RawMessage{
			"image": json.RawMessage(`"registry.example/daemon:latest"`),
			"metro": json.RawMessage(`"sfo"`),
		},
	}
}

func validateProvisioningForTest(
	poolDefault executionstore.MachineProvisioningConfig,
	machine executionstore.MachineProvisioningConfig,
	providerConfig json.RawMessage,
) error {
	maxCPU := 8
	maxMemoryMB := 8192
	return (Definition{}).ValidateMachineProvisioning(
		executionstore.MachinePoolProviderPolicy{
			DefaultProvisioning: poolDefault,
			ResourceLimits: executionstore.MachineResourceLimits{
				MaxTotalCPU:        &maxCPU,
				MaxTotalMemoryMB:   &maxMemoryMB,
				MaxMachineCPU:      &maxCPU,
				MaxMachineMemoryMB: &maxMemoryMB,
			},
			ProviderConfig: providerConfig,
		},
		machine,
	)
}
