package modelcontext

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/jsonschema"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func appContextFixture(t *testing.T, source string) (*fakeContextStore, string) {
	t.Helper()
	appID, err := publicid.Encode(publicid.KindProjectApp, testIDN(940))
	require.NoError(t, err)
	apps := map[string]agentconfig.AppResolution{appID: {AppID: appID, Definition: appdefinition.Slack}}
	compiled, err := agentconfig.Compile(agentconfig.SourceFormatYAML, []byte(`
instruction: Help the user.
model:
  provider_config: deterministic-test
  name: deterministic-owned-kernel-test
`+source), agentconfig.CompileOptions{ResolveAppName: func(name string) (agentconfig.AppResolution, error) {
		require.Equal(t, "engineering", name)
		return apps[appID], nil
	}})
	require.NoError(t, err)
	record := testAgentConfigRecord()
	record.CompiledDefinition, record.EffectiveDefinitionHash = compiled.CanonicalJSON, compiled.Hash
	record.CompilerVersion = compiled.CompilerVersion
	return &fakeContextStore{watermark: 1, hasConfig: true, config: record, appDefinitions: apps}, appID
}

func buildAppContext(t *testing.T, store Store) Bundle {
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

func TestBuildPreparesNamespacedToolWithFixedConfig(t *testing.T) {
	store, appID := appContextFixture(t, `tools:
  app__engineering__post_message:
    permission: {mode: always_ask}
    config: {channel_id: C123, thread_ts: "111.222"}
  app__engineering__read: {}
interaction_handlers:
  engineering: {config: {channel_id: C789}}
`)
	original := append(json.RawMessage(nil), store.config.CompiledDefinition...)
	hash := store.config.EffectiveDefinitionHash
	bundle := buildAppContext(t, NewStore(store, store, store))
	fixed := requireToolSpec(t, bundle.ToolSpecs, "app__engineering__post_message")
	require.Equal(t, toolcatalog.ToolTypeBuiltIn, fixed.Type)
	require.Equal(t, toolpermission.ModeAlwaysAsk, fixed.Permission.Mode)
	require.Contains(t, fixed.Description, "C123")
	require.Contains(t, fixed.Description, "111.222")
	require.NotContains(t, fixed.Description, "C456")
	require.NotContains(t, fixed.Description, "C789")
	require.NoError(t, jsonschema.Validate(fixed.InputSchema, []byte(`{"text":"hello"}`)))
	for _, invalid := range []string{
		`{"text":"hello","channel_id":"C456"}`, `{"text":"hello","thread_ts":"333.444"}`,
		`{"text":"hello","resource":"engineering"}`,
	} {
		require.Error(t, jsonschema.Validate(fixed.InputSchema, []byte(invalid)))
	}
	flexible := requireToolSpec(t, bundle.ToolSpecs, "app__engineering__read")
	require.NoError(t, jsonschema.Validate(flexible.InputSchema, []byte(`{"channel_id":"C456","thread_ts":"333.444"}`)))
	require.Error(t, jsonschema.Validate(flexible.InputSchema, []byte(`{}`)))
	require.False(t, HasTool(bundle.ToolSpecs, "slack_post_message"))
	require.Equal(t, []appDefinitionRequest{{ProjectID: testProjectID, IDs: []string{appID}}}, store.appDefinitionRequests,
		"one project-scoped metadata read deduplicates tools, listener and handler")
	require.Equal(t, original, store.config.CompiledDefinition)
	require.Equal(t, hash, store.config.EffectiveDefinitionHash)
	var compiled agentconfig.Compiled
	require.NoError(t, json.Unmarshal(store.config.CompiledDefinition, &compiled))
	require.Equal(t, appID, compiled.Tools[fixed.Name].AppID)
	require.Empty(t, compiled.Tools[fixed.Name].InputSchema, "effective schemas are not persisted")
	require.NotContains(t, string(original), appdefinition.Slack, "definition metadata is live")
}

func TestBuildOmitsUnavailableAppToolsWithoutChangingStoredConfig(t *testing.T) {
	for _, state := range []string{"disconnected", "deleted", "recreated name"} {
		t.Run(state, func(t *testing.T) {
			store, appID := appContextFixture(t, `tools:
  app__engineering__post_message: {config: {channel_id: C123}}
  web_search: {}
`)
			original := append(json.RawMessage(nil), store.config.CompiledDefinition...)
			hash := store.config.EffectiveDefinitionHash
			require.True(t, HasTool(buildAppContext(t, store).ToolSpecs, "app__engineering__post_message"))
			// The AppStore contract omits disconnected/deleted IDs. Recreating the same
			// name supplies a different ID and must not retarget the pinned tool.
			delete(store.appDefinitions, appID)
			if state == "recreated name" {
				replacement, err := publicid.Encode(publicid.KindProjectApp, testIDN(942))
				require.NoError(t, err)
				store.appDefinitions[replacement] = agentconfig.AppResolution{AppID: replacement, Definition: appdefinition.Slack}
			}
			bundle := buildAppContext(t, store)
			require.False(t, HasTool(bundle.ToolSpecs, "app__engineering__post_message"))
			require.True(t, HasTool(bundle.ToolSpecs, "web_search"), "unrelated capabilities remain usable")
			require.True(t, HasTool(bundle.ToolSpecs, toolcatalog.ToolNameListInteractionHandlers))
			require.Equal(t, original, store.config.CompiledDefinition)
			require.Equal(t, hash, store.config.EffectiveDefinitionHash)
			require.Equal(t, []string{appID}, store.appDefinitionRequests[len(store.appDefinitionRequests)-1].IDs)
		})
	}
}

func TestBuildInteractionSelectionIndependentOfHandlerPageWithoutImplicitSend(t *testing.T) {
	store, appID := appContextFixture(t, "interaction_handlers: {engineering: {}}\n")
	args := json.RawMessage(`{"channel_id":"C123","thread_ts":"111.222"}`)
	store.interactionHandlers = agentconfig.InteractionHandlerPage{
		Selection: &agentconfig.HandlerSelection{Handler: "engineering", AppID: appID, Args: args},
		// A bounded first page can exclude the selected handler.
		Handlers: []agentconfig.InteractionHandlerEntry{{Handler: "another", Description: "Another app"}}, NextCursor: "next",
	}
	bundle := buildAppContext(t, store)
	require.Equal(t, &InteractionDestinationRef{Handler: "engineering", Args: args}, bundle.InteractionRouting.Destination)
	content := InteractionRoutingContent(bundle.InteractionRouting)
	require.Contains(t, content, `"handler":"engineering"`)
	require.Contains(t, content, `"channel_id":"C123"`)
	require.Contains(t, content, `"thread_ts":"111.222"`)
	require.Contains(t, content, "Omnara dashboard")
	require.Contains(t, content, "set_interaction_handler")
	require.NotContains(t, content, appID)
	require.NotContains(t, content, testIDN(940).String())
	require.False(t, HasTool(bundle.ToolSpecs, "app__engineering__post_message"), "a handler grants no send tool")
	require.False(t, HasTool(bundle.ToolSpecs, "send_integration_message"))
	require.True(t, HasTool(bundle.ToolSpecs, toolcatalog.ToolNameListInteractionHandlers))
	require.True(t, HasTool(bundle.ToolSpecs, toolcatalog.ToolNameSetInteractionHandler))
	require.Equal(
		t,
		[]handlerListRequest{{ProjectID: testProjectID, AgentID: testAgentID, Limit: 1}},
		store.interactionHandlerRequests,
	)
	store.interactionHandlers.Handlers = nil
	require.Equal(t, bundle.InteractionRouting, buildAppContext(t, store).InteractionRouting,
		"selection is returned independently even when the page is empty")
	// Storage revokes an unavailable selection by returning nil, not by omitting
	// the handler from this page. Other handlers can remain listed.
	store.interactionHandlers.Selection = nil
	store.interactionHandlers.Handlers = []agentconfig.InteractionHandlerEntry{{Handler: "another"}}
	revoked := buildAppContext(t, store)
	require.Nil(t, revoked.InteractionRouting.Destination)
	require.Contains(t, InteractionRoutingContent(revoked.InteractionRouting), "dashboard only")
}

func TestBuildInteractionSelectionCannotRetargetPinnedApp(t *testing.T) {
	store, _ := appContextFixture(t, "interaction_handlers: {engineering: {}}\n")
	replacement, err := publicid.Encode(publicid.KindProjectApp, testIDN(942))
	require.NoError(t, err)
	for _, selection := range []*agentconfig.HandlerSelection{
		{Handler: "engineering", AppID: replacement, Args: json.RawMessage(`{}`)},
		{Handler: "not-configured", AppID: replacement, Args: json.RawMessage(`{}`)},
	} {
		store.interactionHandlers.Selection = selection
		bundle := buildAppContext(t, store)
		require.Nil(t, bundle.InteractionRouting.Destination)
		require.Contains(t, InteractionRoutingContent(bundle.InteractionRouting), "dashboard only")
	}
}

func TestBuildInteractionDefaultsWithoutHandlersAndDashboardContext(t *testing.T) {
	for _, source := range []string{
		"", "tools: {app__engineering__read: {}}\n", "interaction_handlers: {engineering: {}}\n",
	} {
		t.Run(source, func(t *testing.T) {
			store, _ := appContextFixture(t, source)
			bundle := buildAppContext(t, store)
			for _, name := range toolcatalog.InteractionHandlerToolNames() {
				require.True(t, HasTool(bundle.ToolSpecs, name))
			}
			content := InteractionRoutingContent(bundle.InteractionRouting)
			require.Contains(t, content, "dashboard only")
			require.Contains(t, content, "list_interaction_handlers")
			require.Contains(t, content, "set_interaction_handler")
			require.False(t, HasTool(bundle.ToolSpecs, "app__engineering__post_message"))
			if source == "" {
				require.Empty(t, store.interactionHandlerRequests)
			}
		})
	}
}

func TestNewStoreUsesIndependentAppMetadataProvider(t *testing.T) {
	execution, appID := appContextFixture(t, "tools: {app__engineering__read: {}}\n")
	apps := &fakeContextStore{appDefinitions: execution.appDefinitions}
	execution.appDefinitions = nil
	artifacts := &fakeContextStore{artifacts: execution.artifacts}
	bundle := buildAppContext(t, NewStore(execution, artifacts, apps))
	require.True(t, HasTool(bundle.ToolSpecs, "app__engineering__read"))
	require.Empty(t, execution.appDefinitionRequests)
	require.Equal(t, []appDefinitionRequest{{ProjectID: testProjectID, IDs: []string{appID}}}, apps.appDefinitionRequests)
	apps.appDefinitionsErr = errors.New("metadata unavailable")
	_, err := (Builder{Store: NewStore(execution, artifacts, apps)}).Build(t.Context(), BuildInput{
		Now: time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC), ProjectID: testProjectID, AgentID: testAgentID,
		TurnID: testTurnID, OpeningInputIDs: []uuid.UUID{testInputID},
	})
	require.ErrorIs(t, err, apps.appDefinitionsErr)
}
