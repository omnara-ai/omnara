package storagefixture

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/stretchr/testify/require"
)

func DefaultModelInput(orgID, providerID uuid.UUID, name string) modelstore.CreateConfiguredModelInput {
	return modelstore.CreateConfiguredModelInput{
		OrgID: orgID, ModelProviderConfigID: providerID, Name: name, ProviderModelSlug: name,
		ContextWindowTokens: 128000, MaxOutputTokens: new(8192),
	}
}

func SeedModel(
	t testing.TB,
	ctx context.Context,
	models *modelstore.Store,
	projectID uuid.UUID,
	input modelstore.CreateConfiguredModelInput,
) modelstore.ConfiguredModelRecord {
	t.Helper()
	configuredModel, err := models.CreateConfiguredModel(ctx, input)
	require.NoError(t, err, "create test configured model %q", input.Name)
	_, err = models.CreateProjectModelGrant(ctx, modelstore.CreateProjectModelGrantInput{
		OrgID: input.OrgID, ProjectID: projectID, ConfiguredModelID: configuredModel.ID,
	})
	require.NoError(t, err, "grant test configured model %q", input.Name)
	return configuredModel
}

func SeedModelForAgentYAML(
	t testing.TB,
	ctx context.Context,
	models *modelstore.Store,
	orgID, projectID uuid.UUID,
	sourceYAML string,
) modelstore.ConfiguredModelRecord {
	t.Helper()
	source, err := agentconfig.ParseSource(agentconfig.SourceFormatYAML, []byte(sourceYAML))
	require.NoError(t, err, "parse agent config source model")
	providerConfigName, configuredModelName := source.Model.ProviderConfig, source.Model.Name
	if providerConfigName == "" {
		providerConfigName = "openai-prod"
	}
	if configuredModelName == "" {
		configuredModelName = "gpt-test"
	}
	providerConfig, err := models.GetModelProviderConfigByName(ctx, orgID, providerConfigName)
	require.NoError(t, err, "load test provider config %q", providerConfigName)
	input := DefaultModelInput(orgID, providerConfig.ID, configuredModelName)
	input.DefaultMaxOutputTokens = new(4096)
	return SeedModel(t, ctx, models, projectID, input)
}

func SeedModelAndCompileAgentYAML(
	t testing.TB,
	ctx context.Context,
	models *modelstore.Store,
	execution *executionstore.Store,
	orgID, projectID uuid.UUID,
	sourceYAML string,
) agentconfig.Result {
	t.Helper()
	configuredModel := SeedModelForAgentYAML(t, ctx, models, orgID, projectID, sourceYAML)
	compiled, err := agentconfig.Compile(agentconfig.SourceFormatYAML, []byte(sourceYAML), agentconfig.CompileOptions{
		ResolveModelSelection: func(string, string) (agentconfig.ResolvedModelSelection, error) {
			supportsTools := configuredModel.SupportsTools
			return agentconfig.ResolvedModelSelection{
				ConfiguredModelID: configuredModel.ID.String(),
				SupportsTools:     &supportsTools,
			}, nil
		},
		ResolveMachineName: func(machineName string) (string, error) {
			machineID, err := execution.ResolveAgentConfigMachineName(ctx, projectID, machineName)
			if err != nil {
				return "", err
			}
			return publicid.Encode(publicid.KindMachine, machineID)
		},
		ResolveMachinePoolName: func(machinePoolName string) (string, error) {
			machinePoolID, err := execution.ResolveAgentConfigMachinePoolName(ctx, orgID, projectID, machinePoolName)
			if err != nil {
				return "", err
			}
			return publicid.Encode(publicid.KindMachinePool, machinePoolID)
		},
	})
	require.NoError(t, err, "compile resolved agent yaml")
	return compiled
}
