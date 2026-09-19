package integration

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

// AppProfileDerivation separates the authorizing pinned base from the frozen
// launch snapshot. Source stays empty: it would describe the unmodified profile,
// not this derived config. Persistence happens only in successful admission.
type AppProfileDerivation struct {
	BaseConfigID   uuid.UUID                             `json:"base_config_id"`
	BaseConfigHash string                                `json:"base_config_hash"`
	Config         executionstore.CreateAgentConfigInput `json:"config"`
}

func DeriveAppProfileConfig(
	base executionstore.AgentConfigRecord,
	resources map[string]agentconfig.AgentConfigAppResourceSource,
	opts agentconfig.CompileOptions,
) (AppProfileDerivation, error) {
	if base.ID == uuid.Nil || base.ProjectID == uuid.Nil {
		return AppProfileDerivation{}, fmt.Errorf("a pinned project config is required")
	}
	if _, err := agentconfig.RuntimeContractFromCompiled(
		base.CompiledDefinition,
		base.CompilerVersion,
		base.EffectiveDefinitionHash,
	); err != nil {
		return AppProfileDerivation{}, err
	}
	var compiled agentconfig.Compiled
	if err := json.Unmarshal(base.CompiledDefinition, &compiled); err != nil {
		return AppProfileDerivation{}, err
	}
	derived, err := agentconfig.DeriveWithAppResources(compiled, resources, opts)
	if err != nil {
		return AppProfileDerivation{}, err
	}
	encoded, err := agentconfig.EncodeCompiled(derived)
	if err != nil {
		return AppProfileDerivation{}, err
	}
	return AppProfileDerivation{BaseConfigID: base.ID, BaseConfigHash: base.EffectiveDefinitionHash,
		Config: executionstore.CreateAgentConfigInput{OrgID: base.OrgID, ProjectID: base.ProjectID,
			ConfiguredModelID: base.ConfiguredModelID, CompiledDefinition: encoded.CanonicalJSON,
			CompilerVersion: agentconfig.CompilerVersion, EffectiveDefinitionHash: encoded.Hash}}, nil
}
