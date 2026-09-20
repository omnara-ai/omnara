package agentconfigcompile

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

// DeriveAppConfig preserves the base config's pinned identities while resolving
// only the added capabilities through the ordinary project-scoped compiler options.
// The caller persists the result with the launch, never in a separate write.
func DeriveAppConfig(
	ctx context.Context,
	store *storage.Store,
	orgID, projectID uuid.UUID,
	opts agentconfig.CompileOptions,
	base executionstore.AgentConfigRecord,
	capabilities agentconfig.AppCapabilitiesSource,
) (Body, error) {
	var compiled agentconfig.Compiled
	if err := json.Unmarshal(base.CompiledDefinition, &compiled); err != nil {
		return Body{}, fmt.Errorf("decode base compiled agent config: %w", err)
	}
	derived, err := agentconfig.DeriveWithAppCapabilities(
		compiled, capabilities, options(ctx, store, orgID, projectID, opts),
	)
	if err != nil {
		return Body{}, err
	}
	encoded, err := agentconfig.EncodeCompiled(derived)
	if err != nil {
		return Body{}, err
	}
	return Body{
		ConfiguredModelID:  base.ConfiguredModelID,
		CompiledDefinition: json.RawMessage(encoded.CanonicalJSON),
		CompilerVersion:    agentconfig.CompilerVersion,
		DefinitionHash:     encoded.Hash,
	}, nil
}
