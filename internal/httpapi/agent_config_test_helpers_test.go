//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
)

func createHTTPRuntimeAgent(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	orgID, projectID, userID storage.ID,
	name string,
) executionstore.LaunchAgentResult {
	t.Helper()
	return createHTTPRuntimeAgentWithMachineSource(t, ctx, store, orgID, projectID, userID, name, "")
}

func createHTTPRuntimeAgentWithMachineSource(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	orgID, projectID, userID storage.ID,
	name string,
	machineName string,
) executionstore.LaunchAgentResult {
	t.Helper()
	sourceYAML := `instruction: HTTP integration test agent.
model:
  provider_config: openai-prod
  name: http-test
`
	if machineName != "" {
		sourceYAML += `machine_sources:
  - machine_name: ` + machineName + `
    cwd: /work
`
	}
	sourceYAML += `tools:
  run_command:
    permission:
      mode: always_allow
      parameters: {}
  ask_question:
    permission:
      mode: always_ask
      parameters: {}
  lookup_customer:
    type: custom
    description: Look up a customer by email.
    input_schema:
      type: object
      properties:
        email:
          type: string
      required: [email]
`
	compiled := compileHTTPAgentYAMLResolved(t, ctx, store, orgID, projectID, userID, sourceYAML)
	config, err := store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
		ProjectID:               projectID,
		Definition:              json.RawMessage(compiled.CanonicalJSON),
		Source:                  sourceYAML,
		SourceFormat:            "yaml",
		ConfiguredModelID:       parseConfiguredModelID(t, compiled),
		CompiledDefinition:      json.RawMessage(compiled.CanonicalJSON),
		CompilerVersion:         agentconfig.CompilerVersion,
		EffectiveDefinitionHash: compiled.Hash,
	})
	if err != nil {
		t.Fatalf("create %s config: %v", name, err)
	}
	profile, err := store.Execution().CreateAgentProfile(
		ctx,
		executionstore.CreateAgentProfileInput{
			ProjectID:       projectID,
			Name:            name,
			CurrentConfigID: config.ID,
			IdempotencyKey:  "http-profile-" + name,
		},
	)
	if err != nil {
		t.Fatalf("create %s profile: %v", name, err)
	}
	launch, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      projectID,
		ProfileID:      profile.ID,
		AgentConfigID:  profile.CurrentConfigID,
		LaunchedBy:     httpUserPrincipal(userID),
		IdempotencyKey: "http-launch-" + name,
	})
	if err != nil {
		t.Fatalf("launch %s agent: %v", name, err)
	}
	return launch
}

func compileHTTPAgentYAMLResolved(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	orgID, projectID, userID storage.ID,
	sourceYAML string,
) agentconfig.Result {
	t.Helper()
	source, err := agentconfig.ParseSource(agentconfig.SourceFormatYAML, []byte(sourceYAML))
	if err != nil {
		t.Fatalf("parse agent config source: %v", err)
	}
	provider := storagefixture.EnsureModelProvider(t, ctx, store.Models(), store.Secrets(),
		storagefixture.ModelProviderInput{OrgID: orgID, UserID: userID, Name: source.Model.ProviderConfig})
	configuredModel := storagefixture.EnsureModelAccess(t, ctx, store.Models(), projectID,
		storagefixture.DefaultModelInput(orgID, provider.ID, source.Model.Name))
	compiled, err := agentconfig.Compile(agentconfig.SourceFormatYAML, []byte(sourceYAML), agentconfig.CompileOptions{
		ResolveModelSelection: func(
			providerConfigName string,
			configuredModelName string,
		) (agentconfig.ResolvedModelSelection, error) {
			return resolvedHTTPAgentConfigModel(configuredModel), nil
		},
		ResolveMachineName: func(machineName string) (string, error) {
			machineID, err := store.Execution().ResolveAgentConfigMachineName(ctx, projectID, machineName)
			if err != nil {
				return "", err
			}
			return publicid.Encode(publicid.KindMachine, machineID)
		},
	})
	if err != nil {
		t.Fatalf("compile resolved agent config: %v", err)
	}
	return compiled
}

func resolvedHTTPAgentConfigModel(configuredModel modelstore.ConfiguredModelRecord) agentconfig.ResolvedModelSelection {
	supportsTools := configuredModel.SupportsTools
	return agentconfig.ResolvedModelSelection{
		ConfiguredModelID: configuredModel.ID.String(),
		SupportsTools:     &supportsTools,
	}
}

func parseConfiguredModelID(t *testing.T, compiled agentconfig.Result) storage.ID {
	t.Helper()
	id, err := storage.ParseID(compiled.Compiled.Model.ConfiguredModelID)
	if err != nil {
		t.Fatalf("parse compiled configured model id: %v", err)
	}
	return id
}
