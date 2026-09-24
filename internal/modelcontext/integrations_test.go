package modelcontext

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/jsonschema"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func integrationContextFixture(t *testing.T, source string) (*fakeContextStore, uuid.UUID) {
	t.Helper()
	integrationID := testIDN(940)
	integrations := map[uuid.UUID]agentconfig.IntegrationResolution{
		integrationID: {IntegrationID: integrationID, IntegrationType: integrationdefinition.SlackThread},
	}
	compiled, err := agentconfig.Compile(agentconfig.SourceFormatYAML, []byte(`
instruction: Help the user.
model:
  provider_config: deterministic-test
  name: deterministic-owned-kernel-test
`+source), agentconfig.CompileOptions{ResolveIntegrationName: func(name string) (agentconfig.IntegrationResolution, error) {
		require.Equal(t, "engineering", name)
		return integrations[integrationID], nil
	}})
	require.NoError(t, err)
	record := testAgentConfigRecord()
	record.CompiledDefinition, record.EffectiveDefinitionHash = compiled.CanonicalJSON, compiled.Hash
	return &fakeContextStore{
		watermark:              1,
		hasConfig:              true,
		config:                 record,
		integrationDefinitions: integrations,
	}, integrationID
}

func buildIntegrationContext(t *testing.T, store Store) Bundle {
	t.Helper()
	bundle, err := (Builder{Store: store}).Build(t.Context(), BuildInput{
		Now: time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC), ProjectID: testProjectID, AgentID: testAgentID,
		TurnID: testTurnID, OpeningInputIDs: []uuid.UUID{testInputID},
	})
	require.NoError(t, err)
	return bundle
}

func requireToolSpec(t *testing.T, specs []ToolSpec, name string) ToolSpec {
	t.Helper()
	for _, spec := range specs {
		if spec.Name == name {
			return spec
		}
	}
	t.Fatalf("tool %s is absent", name)
	return ToolSpec{}
}

func TestBuildPreparesNamespacedToolsWithStaticSchemas(t *testing.T) {
	store, integrationID := integrationContextFixture(t, `tools:
  int__engineering__post_message:
    permission: {mode: always_ask}
  int__engineering__read: {}
interaction_handlers:
  engineering: {}
`)
	original := append(json.RawMessage(nil), store.config.CompiledDefinition...)
	hash := store.config.EffectiveDefinitionHash
	bundle := buildIntegrationContext(t, NewStore(store, store, store))
	post := requireToolSpec(t, bundle.ToolSpecs, "int__engineering__post_message")
	require.Equal(t, toolcatalog.ToolTypeBuiltIn, post.Type)
	require.Equal(t, toolpermission.ModeAlwaysAsk, post.Permission.Mode)
	require.NoError(t, jsonschema.Validate(post.InputSchema, []byte(`{"text":"hello"}`)))
	read := requireToolSpec(t, bundle.ToolSpecs, "int__engineering__read")
	require.NoError(t, jsonschema.Validate(read.InputSchema, []byte(`{"cursor":"next","limit":25}`)))
	require.NoError(t, jsonschema.Validate(read.InputSchema, []byte(`{}`)))
	require.Equal(
		t,
		[]integrationDefinitionRequest{{ProjectID: testProjectID, IDs: []uuid.UUID{integrationID}}},
		store.integrationDefinitionRequests,
		"one project-scoped metadata read deduplicates tools and handler",
	)
	require.Equal(t, original, store.config.CompiledDefinition)
	require.Equal(t, hash, store.config.EffectiveDefinitionHash)
	var compiled agentconfig.Compiled
	require.NoError(t, json.Unmarshal(store.config.CompiledDefinition, &compiled))
	require.Equal(t, integrationID, compiled.Tools[post.Name].IntegrationID)
	require.Empty(t, compiled.Tools[post.Name].InputSchema, "effective schemas are not persisted")
}

func TestBuildOmitsUnavailableIntegrationToolsWithoutChangingStoredConfig(t *testing.T) {
	for _, state := range []string{"unavailable", "recreated name"} {
		t.Run(state, func(t *testing.T) {
			store, integrationID := integrationContextFixture(t, `tools:
  int__engineering__post_message: {}
  web_search: {}
`)
			original := append(json.RawMessage(nil), store.config.CompiledDefinition...)
			hash := store.config.EffectiveDefinitionHash
			require.True(t, HasTool(buildIntegrationContext(t, store).ToolSpecs, "int__engineering__post_message"))
			delete(store.integrationDefinitions, integrationID)
			if state == "recreated name" {
				replacement := testIDN(942)
				store.integrationDefinitions[replacement] = agentconfig.IntegrationResolution{
					IntegrationID: replacement, IntegrationType: integrationdefinition.SlackThread,
				}
			}
			bundle := buildIntegrationContext(t, store)
			require.False(t, HasTool(bundle.ToolSpecs, "int__engineering__post_message"))
			require.True(t, HasTool(bundle.ToolSpecs, "web_search"), "unrelated capabilities remain usable")
			require.False(t, HasTool(bundle.ToolSpecs, toolcatalog.ToolNameListInteractionHandlers))
			require.Equal(t, original, store.config.CompiledDefinition)
			require.Equal(t, hash, store.config.EffectiveDefinitionHash)
			require.Equal(
				t,
				[]uuid.UUID{integrationID},
				store.integrationDefinitionRequests[len(store.integrationDefinitionRequests)-1].IDs,
			)
		})
	}
}

func TestBuildInteractionToolsFollowConfig(t *testing.T) {
	for _, test := range []struct {
		name, source string
		wantTools    bool
	}{
		{name: "plain config"},
		{name: "integration tool only", source: "tools: {int__engineering__read: {}}\n"},
		{name: "handler only", source: "interaction_handlers: {engineering: {}}\n"},
		{name: "explicit tools", source: `tools:
  list_interaction_handlers: {}
  set_interaction_handler: {}
interaction_handlers: {engineering: {}}
`, wantTools: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, _ := integrationContextFixture(t, test.source)
			bundle := buildIntegrationContext(t, store)
			for _, name := range toolcatalog.InteractionHandlerToolNames() {
				require.Equal(t, test.wantTools, HasTool(bundle.ToolSpecs, name))
			}
		})
	}
}

func TestNewStoreUsesIndependentIntegrationMetadataProvider(t *testing.T) {
	execution, integrationID := integrationContextFixture(t, "tools: {int__engineering__read: {}}\n")
	integrations := &fakeContextStore{integrationDefinitions: execution.integrationDefinitions}
	execution.integrationDefinitions = nil
	artifacts := &fakeContextStore{artifacts: execution.artifacts}
	bundle := buildIntegrationContext(t, NewStore(execution, artifacts, integrations))
	require.True(t, HasTool(bundle.ToolSpecs, "int__engineering__read"))
	require.Empty(t, execution.integrationDefinitionRequests)
	require.Equal(
		t,
		[]integrationDefinitionRequest{{ProjectID: testProjectID, IDs: []uuid.UUID{integrationID}}},
		integrations.integrationDefinitionRequests,
	)
	integrations.integrationDefinitionsErr = errors.New("metadata unavailable")
	_, err := (Builder{Store: NewStore(execution, artifacts, integrations)}).Build(t.Context(), BuildInput{
		Now: time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC), ProjectID: testProjectID, AgentID: testAgentID,
		TurnID: testTurnID, OpeningInputIDs: []uuid.UUID{testInputID},
	})
	require.ErrorIs(t, err, integrations.integrationDefinitionsErr)
}
