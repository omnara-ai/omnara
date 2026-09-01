package blaxel

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestBlaxelDefinitionValidation(t *testing.T) {
	valid := testMachineProvisioning(t, nil)
	cpu := 1
	memoryMB := 1024
	missingRegion := testMachineProvisioning(t, map[string]any{"region": ""})
	sleeping := testMachineProvisioning(t, map[string]any{"sleep_after_ms": 30000})
	sleepTooShort := testMachineProvisioning(t, map[string]any{"sleep_after_ms": 29999})
	sleepTooLong := testMachineProvisioning(
		t,
		map[string]any{"sleep_after_ms": int(daemonprotocol.MaximumSleepAfterMS + 1)},
	)
	oversized := testMachineProvisioning(
		t,
		map[string]any{"startup_script": strings.Repeat("x", 64*1024+1)},
	)
	for _, test := range []struct {
		name              string
		machine           executionstore.MachineProvisioningConfig
		providerConfig    string
		wantErrorContains string
	}{
		{name: "valid", machine: valid, providerConfig: `{"workspace":"omnara"}`},
		{name: "sleep enabled", machine: sleeping, providerConfig: `{"workspace":"omnara"}`},
		{
			name:              "sleep too short",
			machine:           sleepTooShort,
			providerConfig:    `{"workspace":"omnara"}`,
			wantErrorContains: "sleep_after_ms must be at least 30000",
		},
		{
			name:              "sleep too long",
			machine:           sleepTooLong,
			providerConfig:    `{"workspace":"omnara"}`,
			wantErrorContains: "sleep_after_ms must be at most 9223372036854",
		},
		{
			name:           "wildcard allowlists",
			machine:        valid,
			providerConfig: `{"workspace":"omnara","allowed_images":["*"],"allowed_regions":["*"]}`,
		},
		{
			name: "cpu",
			machine: executionstore.MachineProvisioningConfig{
				CPU: &cpu, MemoryMB: &memoryMB, ProviderOptions: valid.ProviderOptions,
			},
			providerConfig:    `{"workspace":"omnara"}`,
			wantErrorContains: "does not support cpu",
		},
		{
			name:              "missing workspace",
			machine:           valid,
			providerConfig:    `{}`,
			wantErrorContains: "workspace",
		},
		{
			name:              "missing region",
			machine:           missingRegion,
			providerConfig:    `{"workspace":"omnara"}`,
			wantErrorContains: "requires region",
		},
		{
			name:              "invalid workspace",
			machine:           valid,
			providerConfig:    `{"workspace":"Not Valid"}`,
			wantErrorContains: "valid DNS label",
		},
		{
			name:              "unknown config field",
			machine:           valid,
			providerConfig:    `{"workspace":"omnara","extra":true}`,
			wantErrorContains: "unknown field",
		},
		{
			name:              "empty allowlist",
			machine:           valid,
			providerConfig:    `{"workspace":"omnara","allowed_images":[]}`,
			wantErrorContains: "must not be empty",
		},
		{
			name:              "oversized startup script",
			machine:           oversized,
			providerConfig:    `{"workspace":"omnara"}`,
			wantErrorContains: "startup_script must be at most",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateProvisioningForTest(
				valid,
				test.machine,
				json.RawMessage(test.providerConfig),
			)
			if test.wantErrorContains == "" && err != nil {
				t.Fatalf("validate pool: %v", err)
			}
			if test.wantErrorContains != "" &&
				(err == nil || !strings.Contains(err.Error(), test.wantErrorContains)) {
				t.Fatalf("validation error = %v, want %q", err, test.wantErrorContains)
			}
		})
	}

	otherImage := testMachineProvisioning(t, map[string]any{"image": "blaxel/other:latest"})
	err := validateProvisioningForTest(
		valid,
		otherImage,
		json.RawMessage(`{"workspace":"omnara"}`),
	)
	if err == nil || !strings.Contains(err.Error(), "allowed_images") {
		t.Fatalf("default image allowlist error = %v", err)
	}
}

func validateProvisioningForTest(
	poolDefault executionstore.MachineProvisioningConfig,
	machine executionstore.MachineProvisioningConfig,
	providerConfig json.RawMessage,
) error {
	maxMemoryMB := 8192
	return (Definition{}).ValidateMachineProvisioning(
		executionstore.MachinePoolProviderPolicy{
			DefaultProvisioning: poolDefault,
			ResourceLimits: executionstore.MachineResourceLimits{
				MaxTotalMemoryMB:   &maxMemoryMB,
				MaxMachineMemoryMB: &maxMemoryMB,
			},
			ProviderConfig: providerConfig,
		},
		machine,
	)
}

func TestDefinitionCreatesBlaxel(t *testing.T) {
	runtimeProvider, err := (Definition{}).NewProvider(
		json.RawMessage(`{"workspace":"omnara"}`),
		providers.RuntimeConfig{
			Omnara: providers.ManagedMachineEndpoints{
				APIURL:       "https://api.omnara.test/v1",
				InstallerURL: "https://app.omnara.test/install/omnarad.sh",
			},
			ProviderAuthToken: "token",
		},
	)
	if err != nil {
		t.Fatalf("create blaxel provider: %v", err)
	}
	created, ok := runtimeProvider.(*provider)
	if !ok || created.workspace != "omnara" || created.apiToken != "token" {
		t.Fatalf("provider = %#v", runtimeProvider)
	}
}
