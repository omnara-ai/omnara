package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func DeriveIntegrationProfileConfig(
	base executionstore.AgentConfigRecord,
	additions agentconfig.IntegrationCapabilitiesSource,
	opts agentconfig.CompileOptions,
) (executionstore.CreateAgentConfigInput, error) {
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
	derived, err := agentconfig.DeriveWithIntegrationCapabilities(compiled, additions, opts)
	if errors.Is(err, agentconfig.ErrIntegrationCapabilityUnavailable) {
		return executionstore.CreateAgentConfigInput{}, fmt.Errorf("%w: %w", ErrIntegrationLaunchUnavailable, err)
	}
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

type integrationLaunchProfileReader interface {
	GetAgentProfile(context.Context, uuid.UUID, uuid.UUID) (executionstore.AgentProfileRecord, error)
}

func integrationLaunchProfile(
	ctx context.Context, execution integrationLaunchProfileReader,
	integration integrationstore.IntegrationRecord, profileID uuid.UUID,
) (executionstore.AgentProfileRecord, error) {
	profile, err := execution.GetAgentProfile(ctx, integration.ProjectID, profileID)
	if storeerr.IsNotFound(err) {
		return profile, fmt.Errorf("configured launcher profile %s is unavailable: %w",
			profileID, ErrIntegrationLaunchUnavailable)
	}
	if err != nil {
		return profile, err
	}
	_, err = deriveIntegrationLaunchConfig(profile.CurrentConfig, integration)
	return profile, err
}
