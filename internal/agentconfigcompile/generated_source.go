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

func GenerateSource(
	ctx context.Context,
	store *storage.Store,
	orgID, projectID uuid.UUID,
	config executionstore.AgentConfigRecord,
) (string, error) {
	var compiled agentconfig.Compiled
	if err := json.Unmarshal(config.CompiledDefinition, &compiled); err != nil {
		return "", fmt.Errorf("decode compiled agent config: %w", err)
	}
	source, err := agentconfig.SourceFromCompiled(compiled, agentconfig.SourceNames{
		Model: func(id uuid.UUID) (string, string, error) {
			model, err := store.Models().GetConfiguredModelDisplay(ctx, orgID, id)
			if err != nil {
				return "", "", err
			}
			revision, err := store.Models().GetConfiguredModelRevisionDisplay(ctx, orgID, model.CurrentRevisionID)
			return revision.ProviderConfigName, revision.ConfiguredModelName, err
		},
		Machine: func(id uuid.UUID) (string, error) {
			machine, err := store.Execution().GetMachine(ctx, orgID, id)
			return machine.DisplayName, err
		},
		MachinePool: func(id uuid.UUID) (string, error) {
			pool, err := store.Execution().GetMachinePoolForLifecycle(ctx, orgID, id)
			return pool.Name, err
		},
		Profile: func(id uuid.UUID) (string, error) {
			return store.Execution().GetAgentProfileName(ctx, projectID, id)
		},
		MemoryStore: func(id uuid.UUID) (string, error) {
			return store.Memories().Name(ctx, projectID, id)
		},
	})
	if err != nil {
		return "", err
	}
	return agentconfig.EncodeSourceYAML(source)
}
