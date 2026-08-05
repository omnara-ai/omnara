package blaxel

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const testStartupScriptEnvVar = "OMNARA_STARTUP_SCRIPT_PAYLOAD"

func testInstallationID() storage.ID {
	return uuid.MustParse("00000000-0000-0000-0000-000000000002")
}

func testMachineProvisioning(
	t *testing.T,
	providerOptionOverrides map[string]any,
) executionstore.MachineProvisioningConfig {
	t.Helper()
	options := map[string]json.RawMessage{
		"image":  mustRawJSON(t, "blaxel/base-image:latest"),
		"region": mustRawJSON(t, "us-pdx-1"),
	}
	for key, value := range providerOptionOverrides {
		options[key] = mustRawJSON(t, value)
	}
	memoryMB := 1024
	return executionstore.MachineProvisioningConfig{
		MemoryMB: &memoryMB, ProviderOptions: options,
	}
}

func mustRawJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return raw
}

func sandboxEnvMap(envs []sandboxEnv) map[string]string {
	env := make(map[string]string, len(envs))
	for _, entry := range envs {
		env[entry.Name] = entry.Value
	}
	return env
}
