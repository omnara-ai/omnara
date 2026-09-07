package storagefixture

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/stretchr/testify/require"
)

// SeedAgentConfig provisions the model and grant, compiles YAML, and persists the
// config in the supplied project. The caller chooses when to activate it or
// create a profile pointing to it.
func SeedAgentConfig(
	t testing.TB,
	ctx context.Context,
	models *modelstore.Store,
	execution *executionstore.Store,
	orgID, projectID uuid.UUID,
	sourceYAML string,
) executionstore.AgentConfigRecord {
	t.Helper()
	compiled := SeedModelAndCompileAgentYAML(t, ctx, models, execution, orgID, projectID, sourceYAML)
	modelID, err := uuid.Parse(compiled.Compiled.Model.ConfiguredModelID)
	require.NoError(t, err, "parse compiled configured model ID for project %s", projectID)
	config, err := execution.CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
		ProjectID:               projectID,
		Definition:              json.RawMessage(compiled.CanonicalJSON),
		Source:                  sourceYAML,
		ConfiguredModelID:       modelID,
		CompiledDefinition:      json.RawMessage(compiled.CanonicalJSON),
		CompilerVersion:         agentconfig.CompilerVersion,
		EffectiveDefinitionHash: compiled.Hash,
	})
	require.NoError(t, err, "create agent config for project %s", projectID)
	return config
}
