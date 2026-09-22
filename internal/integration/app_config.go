package integration

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func DeriveAppProfileConfig(
	base executionstore.AgentConfigRecord,
	additions agentconfig.AppCapabilitiesSource,
	opts agentconfig.CompileOptions,
) (executionstore.CreateAgentConfigInput, error) {
	// Source stays empty because the original source would describe the unmodified profile.
	if base.ID == uuid.Nil || base.ProjectID == uuid.Nil {
		return executionstore.CreateAgentConfigInput{}, fmt.Errorf("a pinned project config is required")
	}
	if _, err := agentconfig.RuntimeContractFromCompiled(
		base.CompiledDefinition,
		base.EffectiveDefinitionHash,
	); err != nil {
		return executionstore.CreateAgentConfigInput{}, err
	}
	var compiled agentconfig.Compiled
	if err := json.Unmarshal(base.CompiledDefinition, &compiled); err != nil {
		return executionstore.CreateAgentConfigInput{}, err
	}
	derived, err := agentconfig.DeriveWithAppCapabilities(compiled, additions, opts)
	if err != nil {
		return executionstore.CreateAgentConfigInput{}, err
	}
	encoded, err := agentconfig.EncodeCompiled(derived)
	if err != nil {
		return executionstore.CreateAgentConfigInput{}, err
	}
	return executionstore.CreateAgentConfigInput{OrgID: base.OrgID, ProjectID: base.ProjectID,
		ConfiguredModelID: base.ConfiguredModelID, CompiledDefinition: encoded.CanonicalJSON,
		EffectiveDefinitionHash: encoded.Hash}, nil
}
