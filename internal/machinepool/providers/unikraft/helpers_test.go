package unikraft

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const testStartupScriptEnvVar = "OMNARA_STARTUP_SCRIPT_PAYLOAD"

func testInstallationID() storage.ID {
	return uuid.MustParse("00000000-0000-0000-0000-000000000002")
}

func newTestRESTClient(baseURL, token string, httpClient *http.Client) (*restClient, error) {
	normalizedBaseURL, err := normalizeAPIBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: providers.HTTPClientTimeout}
	}
	return &restClient{baseURL: normalizedBaseURL, apiToken: token, httpClient: httpClient}, nil
}

func mustRawJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return raw
}

func decodeManagedBootstrapScript(t *testing.T, encoded string) string {
	t.Helper()
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode bootstrap script: %v", err)
	}
	return string(decoded)
}

func testMachineProvisioning(t *testing.T, overrides map[string]any) executionstore.MachineProvisioningConfig {
	t.Helper()
	cpu := 1
	memoryMB := 1024
	machineProvisioning := executionstore.MachineProvisioningConfig{
		CPU:      &cpu,
		MemoryMB: &memoryMB,
		ProviderOptions: map[string]json.RawMessage{
			"image": mustRawJSON(t, "registry.example/daemon:latest"),
			"metro": mustRawJSON(t, "sfo"),
		},
	}
	for key, value := range overrides {
		switch key {
		case "provider_options":
			optionOverrides, ok := value.(map[string]any)
			if !ok {
				t.Fatalf("provider_options override has unexpected type: %+v", value)
			}
			for optionKey, optionValue := range optionOverrides {
				machineProvisioning.ProviderOptions[optionKey] = mustRawJSON(t, optionValue)
			}
		default:
			t.Fatalf("unsupported machine provisioning override %q", key)
		}
	}
	return machineProvisioning
}

func mustInstanceName(t *testing.T, machineID storage.ID) string {
	t.Helper()
	name, err := providers.MachineAllocationName(testInstallationID(), machineID)
	if err != nil {
		t.Fatalf("derive instance name: %v", err)
	}
	return name
}
