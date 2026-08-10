package machinepool

import (
	"encoding/json"
	"fmt"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/machinepool/providers/blaxel"
	"github.com/omnara-ai/omnara/internal/machinepool/providers/daytona"
	"github.com/omnara-ai/omnara/internal/machinepool/providers/unikraft"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

var _ executionstore.MachinePoolProviders = Catalog{}

type Catalog struct {
	definitions map[string]providers.Definition
}

func DefaultCatalog() Catalog {
	return Catalog{
		definitions: map[string]providers.Definition{
			providers.Blaxel:   blaxel.Definition{},
			providers.Daytona:  daytona.Definition{},
			providers.Unikraft: unikraft.Definition{},
		},
	}
}

func (c Catalog) definition(provider string) (providers.Definition, bool) {
	definition, ok := c.definitions[provider]
	return definition, ok
}

func (c Catalog) ResolveMachineProviderOptions(
	provider string,
	defaultOptions map[string]json.RawMessage,
	projectOptions map[string]json.RawMessage,
	agentOptions map[string]json.RawMessage,
) (map[string]json.RawMessage, error) {
	definition, ok := c.definition(provider)
	if !ok {
		return nil, fmt.Errorf("machine provider %q is not configured", provider)
	}
	return definition.ResolveMachineProviderOptions(defaultOptions, projectOptions, agentOptions), nil
}

func (c Catalog) ValidatePool(
	provider string,
	policy executionstore.MachinePoolProviderPolicy,
) error {
	definition, ok := c.definition(provider)
	if !ok {
		return fmt.Errorf("machine provider %q is not configured", provider)
	}
	if policy.RuntimeProtectionEnabled {
		if _, ok := definition.(providers.RuntimeProviderDefinition); !ok {
			return fmt.Errorf("machine provider %q does not support runtime protection", provider)
		}
	}
	return definition.ValidatePool(policy)
}

func (c Catalog) BuildMachineProvisioningIntent(
	provider string,
	policy executionstore.MachinePoolProviderPolicy,
	machineProvisioning executionstore.MachineProvisioningConfig,
) (executionstore.MachineProvisioningConfig, error) {
	definition, ok := c.definition(provider)
	if !ok {
		return executionstore.MachineProvisioningConfig{}, fmt.Errorf(
			"machine provider %q is not configured",
			provider,
		)
	}
	return definition.BuildMachineProvisioningIntent(policy, machineProvisioning)
}
