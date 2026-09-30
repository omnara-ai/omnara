package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/omnara-ai/omnara/internal/testutil/integrationtest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

type integrationPlanExecution struct {
	IntegrationExecutionStore
	profile executionstore.AgentProfileRecord
	reads   int
	configs map[uuid.UUID]executionstore.AgentConfigRecord
}

func (s *integrationPlanExecution) CreateAgentConfig(
	_ context.Context, input executionstore.CreateAgentConfigInput,
) (executionstore.AgentConfigRecord, error) {
	if s.configs == nil {
		s.configs = map[uuid.UUID]executionstore.AgentConfigRecord{}
	}
	for _, config := range s.configs {
		if config.EffectiveDefinitionHash == input.EffectiveDefinitionHash {
			return config, nil
		}
	}
	config := executionstore.AgentConfigRecord{
		ID: uuid.New(), ProjectID: input.ProjectID, Source: input.Source,
		CompiledDefinition: input.CompiledDefinition, EffectiveDefinitionHash: input.EffectiveDefinitionHash,
	}
	s.configs[config.ID] = config
	return config, nil
}

func (s *integrationPlanExecution) GetAgentInProject(
	_ context.Context, projectID, id uuid.UUID,
) (executionstore.AgentRecord, error) {
	return executionstore.AgentRecord{ID: id, ProjectID: projectID, AgentProfileID: s.profile.ID}, nil
}

func (s *integrationPlanExecution) GetAgentProfile(
	context.Context,
	uuid.UUID,
	uuid.UUID,
) (executionstore.AgentProfileRecord, error) {
	s.reads++
	return s.profile, nil
}

type integrationPlanStore struct {
	IntegrationRoutingStore
	integrationSetup integrationstore.IntegrationRecord
	receipt          integrationstore.IntegrationInboxRecord
}

func (s *integrationPlanStore) GetIntegrationByID(
	context.Context,
	uuid.UUID,
) (integrationstore.IntegrationRecord, error) {
	return s.integrationSetup, nil
}

func (s *integrationPlanStore) GetIntegrationInbox(
	context.Context,
	uuid.UUID,
	uuid.UUID,
) (integrationstore.IntegrationInboxRecord, error) {
	return s.receipt, nil
}

func integrationPlannerFixture(
	t *testing.T,
) (
	*IntegrationRouter,
	*integrationPlanExecution,
	*integrationPlanStore,
	integrationstore.IntegrationRecord,
	IntegrationEvent,
) {
	t.Helper()
	project, org, integrationID := uuid.New(), uuid.New(), uuid.New()
	modelID := uuid.New()
	signingSecretID, err := publicid.Encode(publicid.KindSecret, uuid.New())
	require.NoError(t, err)
	disabled := false
	source := agentconfig.AgentConfigSource{
		Instruction: "Pinned original",
		Model:       agentconfig.AgentConfigModelSource{ProviderConfig: "test", Name: "model"},
		EventWebhook: &agentconfig.EventWebhook{
			URL: "https://example.com/events", Events: []string{"model_output"}, SigningSecretID: signingSecretID,
		},
		Tools: map[string]agentconfig.AgentConfigToolSource{
			toolcatalog.IntegrationToolName("chat", "post_message"): {Enabled: &disabled},
		},
	}
	raw, err := json.Marshal(source)
	require.NoError(t, err)
	compiled, err := agentconfig.Compile(agentconfig.SourceFormatJSON, raw, agentconfig.CompileOptions{
		ResolveModelSelection: func(string, string) (agentconfig.ResolvedModelSelection, error) {
			return agentconfig.ResolvedModelSelection{ConfiguredModelID: modelID}, nil
		},
		ResolveIntegrationName: func(string) (agentconfig.IntegrationResolution, error) {
			return agentconfig.IntegrationResolution{
				IntegrationID:   integrationID,
				IntegrationKind: integrationdefinition.SlackThread,
			}, nil
		},
	})
	require.NoError(t, err)
	base := executionstore.AgentConfigRecord{
		ID:                      uuid.New(),
		OrgID:                   org,
		ProjectID:               project,
		Source:                  "deliberately unusable source",
		ConfiguredModelID:       modelID,
		CompiledDefinition:      compiled.CanonicalJSON,
		EffectiveDefinitionHash: compiled.Hash,
	}
	execution := &integrationPlanExecution{
		profile: executionstore.AgentProfileRecord{
			ID:              uuid.New(),
			ProjectID:       project,
			CurrentConfigID: base.ID,
			CurrentConfig:   base,
		},
	}
	integrations := &integrationPlanStore{
		integrationSetup: integrationstore.IntegrationRecord{
			ID:   integrationID,
			Name: "chat", IntegrationKind: integrationdefinition.SlackThread,
			OrgID:             org,
			ProjectID:         project,
			Provider:          "slack",
			ProviderTenantID:  "T123",
			State:             integrationstore.IntegrationStateActive,
			InstalledByUserID: uuid.New(),
		},
	}
	integrations.receipt = integrationstore.IntegrationInboxRecord{
		ID:            uuid.New(),
		ProjectID:     project,
		IntegrationID: integrations.integrationSetup.ID,
	}
	integration := integrations.integrationSetup
	integration.Settings = integrationtest.ChatSettings("", execution.profile.ID)
	event := IntegrationEvent{
		Event: integrationdefinition.Event{
			Scope:     integrationdefinition.Scope{Slack: &integrationdefinition.SlackScope{ChannelID: "C123", ThreadTS: "1.2"}},
			Kind:      "message",
			Mentioned: true,
		},
		SemanticKey:   "message:1",
		ContentBlocks: json.RawMessage(`[{"type":"text","text":"hello"}]`),
		Actor:         integrationTestActor(t, integration, "U123"),
	}
	return NewIntegrationRouter(execution, integrations), execution, integrations, integration, event
}

func TestIntegrationPlanPinsFullProfileMembershipAndCompiledPolicy(t *testing.T) {
	router, execution, integrations, integration, event := integrationPlannerFixture(t)
	var base agentconfig.Compiled
	require.NoError(t, json.Unmarshal(execution.profile.CurrentConfig.CompiledDefinition, &base))
	oldID := uuid.Must(uuid.NewV7())
	event.ContentBlocks = json.RawMessage(
		`[{"type":"text","text":"review"},{"type":"media_ref","artifact_id":"` + oldID.String() + `"}]`,
	)
	event.Files = []executionstore.InboxPlannedFile{{ArtifactID: oldID, ProviderFileID: "F123"}}
	request, err := prepareIntegrationEvent(event, integrations.integrationSetup)
	require.NoError(t, err)
	request.candidates.Launcher = &integration
	applyTestIntegrationLaunchPolicy(t, integrations.receipt, integrations.integrationSetup, &request)
	plan, err := router.buildIntegrationPlan(t.Context(), integrations.receipt, integrations.integrationSetup, request)
	require.NoError(t, err)
	require.Len(t, plan.Recipients, 1)
	require.Equal(t, 1, execution.reads)
	agents, artifacts := map[uuid.UUID]bool{}, map[uuid.UUID]bool{}
	for _, recipient := range plan.Recipients {
		_, files, err := plan.Message.RecipientContent(recipient.ArtifactIDs)
		require.NoError(t, err)
		require.Equal(t, uuid.Version(7), recipient.AgentID.Version())
		require.False(t, agents[recipient.AgentID])
		agents[recipient.AgentID] = true
		require.Equal(t, execution.profile.CurrentConfig.ID, recipient.Launch.DerivedBaseConfigID)
		require.Empty(t, execution.configs[recipient.Launch.AgentConfigID].Source)
		require.Len(t, files, 1)
		require.False(t, artifacts[files[0].ArtifactID])
		artifacts[files[0].ArtifactID] = true
		require.NotEqual(t, oldID, files[0].ArtifactID)
		var compiled agentconfig.Compiled
		require.NoError(t, json.Unmarshal(execution.configs[recipient.Launch.AgentConfigID].CompiledDefinition, &compiled))
		require.Equal(t, "Pinned original", compiled.Instruction)
		require.Equal(t, base.Model, compiled.Model)
		require.Equal(t, base.EventWebhook, compiled.EventWebhook)
		require.False(t, compiled.Tools[toolcatalog.IntegrationToolName("chat", "post_message")].Enabled)
		require.Len(t, recipient.Launch.Subscriptions, 1)
		subscription := recipient.Launch.Subscriptions[0]
		require.Equal(t, integration.ID, subscription.IntegrationID)
		require.JSONEq(t, `{"channel_id":"C123","thread_ts":"1.2"}`, string(subscription.Conversation))
		require.Equal(
			t,
			integration.ID,
			compiled.Tools[toolcatalog.IntegrationToolName(integration.Name, "read")].IntegrationID,
		)
		require.Equal(t, integration.ID, compiled.InteractionHandlers[integration.Name].IntegrationID)
		raw, err := json.Marshal(recipient)
		require.NoError(t, err)
		var kernel executionstore.InboxLaunchRecipient
		require.NoError(t, json.Unmarshal(raw, &kernel))
		require.Equal(t, *recipient.LaunchClaim, kernel.LaunchClaim)
		require.Equal(t, recipient.AgentID, kernel.AgentID)
		require.Equal(t, recipient.Launch.Subscriptions, kernel.Launch.Subscriptions)
		require.Equal(t, execution.configs[recipient.Launch.AgentConfigID], execution.configs[kernel.Launch.AgentConfigID])
		contract, err := agentconfig.RuntimeContractFromCompiled(
			execution.configs[kernel.Launch.AgentConfigID].CompiledDefinition,
			execution.configs[kernel.Launch.AgentConfigID].EffectiveDefinitionHash,
		)
		require.NoError(t, err)
		require.Equal(t, []uuid.UUID{integration.ID}, contract.ReferencedIntegrationIDs())
	}
	integrations.receipt.Plan, err = json.Marshal(plan)
	require.NoError(t, err)
	integrations.integrationSetup.State = integrationstore.IntegrationStateDisconnected
	got, err := router.Freeze(t.Context(), integrations.receipt.Lease(), nil)
	require.NoError(t, err)
	require.Equal(t, plan, got)
	got, err = freezeTestIntegrationEvent(t.Context(), router, integrations.receipt.Lease(), nil)
	require.NoError(t, err, "the policy helper must also leave frozen retries untouched")
	require.Equal(t, plan, got)
}

func TestIntegrationPlanDiscordThreadRequiresExactSubscription(t *testing.T) {
	router, _, integrations, _, event := integrationPlannerFixture(t)
	integrations.integrationSetup.Provider = "discord"
	integrations.integrationSetup.ProviderTenantID = "11"
	event.Event.Scope = integrationdefinition.Scope{
		Discord: &integrationdefinition.DiscordScope{GuildID: "100", ChannelID: "300", ThreadID: "500"},
	}
	event.Event.Mentioned = false
	request, err := prepareIntegrationEvent(event, integrations.integrationSetup)
	require.NoError(t, err)
	parent, exact := uuid.New(), uuid.New()
	request.candidates.Subscriptions = []integrationstore.IntegrationSubscriptionRecord{
		{
			AgentID: parent,
			Address: integrationstore.ConversationAddress{Kind: "channel", Ref: "300"},
		},
		{
			AgentID: exact,
			Address: integrationstore.ConversationAddress{Kind: "thread", Ref: "500"},
		},
	}
	applyTestIntegrationLaunchPolicy(t, integrations.receipt, integrations.integrationSetup, &request)
	plan, err := router.buildIntegrationPlan(t.Context(), integrations.receipt, integrations.integrationSetup, request)
	require.NoError(t, err)
	require.Len(t, plan.Recipients, 1)
	for _, recipient := range plan.Recipients {
		require.Equal(t, exact, recipient.AgentID)
	}
	request.candidates.Subscriptions = request.candidates.Subscriptions[:1]
	applyTestIntegrationLaunchPolicy(t, integrations.receipt, integrations.integrationSetup, &request)
	plan, err = router.buildIntegrationPlan(t.Context(), integrations.receipt, integrations.integrationSetup, request)
	require.NoError(t, err)
	require.Empty(t, plan.Recipients, "parent authority must not subscribe every thread")
}

func TestIntegrationEventRejectsUnpersistableContentBeforePlanning(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*IntegrationEvent)
	}{
		{"text", func(event *IntegrationEvent) {
			event.ContentBlocks = json.RawMessage(`[{"type":"text","text":"before\u0000after"}]`)
		}},
		{"metadata", func(event *IntegrationEvent) {
			event.Metadata = json.RawMessage(`{"nested":["before\u0000after"]}`)
		}},
		{"display name", func(event *IntegrationEvent) { event.DisplayName = "before\x00after" }},
		{"actor", func(event *IntegrationEvent) { event.Actor.DisplayName = new("before\x00after") }},
		{"semantic key", func(event *IntegrationEvent) { event.SemanticKey = "before\x00after" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			router, execution, integrations, _, event := integrationPlannerFixture(t)
			test.edit(&event)
			plan, err := router.Freeze(t.Context(), integrations.receipt.Lease(), &event)
			require.ErrorIs(t, err, ErrIntegrationInboundPermanent)
			require.Empty(t, plan.Recipients)
			require.Zero(t, execution.reads)
			require.Empty(t, execution.configs)
		})
	}
}

func TestIntegrationPlanLaunchesOnlyExplicitIntents(t *testing.T) {
	router, execution, integrations, integration, event := integrationPlannerFixture(t)
	request, err := prepareIntegrationEvent(event, integrations.integrationSetup)
	require.NoError(t, err)
	request.candidates.Launcher = &integration
	plan, err := router.buildIntegrationPlan(t.Context(), integrations.receipt, integrations.integrationSetup, request)
	require.NoError(t, err)
	require.Empty(t, plan.Recipients, "matching saved setups do not authorize an implicit launch")
	require.Zero(t, execution.reads)

	request.event.Event.Mentioned = true
	request.event.Directed = true
	request.event.Launches = []IntegrationLaunchIntent{
		{IntegrationID: integration.ID, LaunchKey: integrationdefinition.ProfileLaunchKey, ProfileID: execution.profile.ID},
	}
	request.candidates.Subscriptions = []integrationstore.IntegrationSubscriptionRecord{
		{AgentID: uuid.New(), Address: request.address},
	}
	plan, err = router.buildIntegrationPlan(t.Context(), integrations.receipt, integrations.integrationSetup, request)
	require.NoError(t, err)
	require.Len(t, plan.Recipients, 1)
	for _, recipient := range plan.Recipients {
		require.NotNil(t, recipient.Launch)
		require.Equal(t, integrationdefinition.ProfileLaunchKey, recipient.LaunchClaim.LaunchKey)
	}
}

func TestIntegrationPlanRejectsUnavailableLaunchIntents(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*integrationEventCandidates)
	}{
		{"absent integration", func(r *integrationEventCandidates) { r.candidates.Launcher = nil }},
		{"disabled integration", func(r *integrationEventCandidates) {
			r.candidates.Launcher.State = integrationstore.IntegrationStateDisconnected
		}},
		{"wrong project", func(r *integrationEventCandidates) { r.candidates.Launcher.ProjectID = uuid.New() }},
		{"removed launcher", func(r *integrationEventCandidates) { r.candidates.Launcher.Settings = json.RawMessage(`{}`) }},
		{"retargeted profile", func(r *integrationEventCandidates) {
			r.candidates.Launcher.Settings = integrationtest.ChatSettings("", uuid.New())
		}},
		{"missing expected recipient", func(r *integrationEventCandidates) { r.event.Launches[0].ProfileID = uuid.Nil }},
		{"retired launch owner", func(r *integrationEventCandidates) {
			now := time.Now()
			r.candidates.LaunchOwners = []integrationstore.IntegrationTargetRecord{{
				IntegrationID: r.event.Launches[0].IntegrationID, LaunchKey: "a", AgentID: uuid.New(), DeletedAt: &now,
			}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			router, execution, integrations, integration, event := integrationPlannerFixture(t)
			event.Launches = []IntegrationLaunchIntent{
				{IntegrationID: integration.ID, LaunchKey: integrationdefinition.ProfileLaunchKey, ProfileID: execution.profile.ID},
			}
			request, err := prepareIntegrationEvent(event, integrations.integrationSetup)
			require.NoError(t, err)
			request.candidates.Launcher = &integration
			test.change(&request)
			plan, err := router.buildIntegrationPlan(t.Context(), integrations.receipt, integrations.integrationSetup, request)
			require.ErrorIs(t, err, ErrIntegrationLaunchUnavailable)
			require.Empty(t, plan.Recipients)
			require.Zero(t, execution.reads, "stale choices must fail before profile derivation")
		})
	}
}

func TestIntegrationLaunchPreservesEntireExistingCapabilities(t *testing.T) {
	_, execution, _, integration, event := integrationPlannerFixture(t)
	base := execution.profile.CurrentConfig
	var compiled agentconfig.Compiled
	require.NoError(t, json.Unmarshal(base.CompiledDefinition, &compiled))
	integrationID := compiled.Tools[toolcatalog.IntegrationToolName(integration.Name, "post_message")].IntegrationID
	toolKey := toolcatalog.IntegrationToolName(integration.Name, "post_message")
	tool := compiled.Tools[toolKey]
	tool.Deferred = true
	compiled.Tools[toolKey] = tool
	compiled.InteractionHandlers = map[string]agentconfig.IntegrationCapabilityCompiled{
		integration.Name: {IntegrationID: integrationID},
	}
	encoded, err := agentconfig.EncodeCompiled(compiled)
	require.NoError(t, err)
	base.CompiledDefinition, base.EffectiveDefinitionHash = encoded.CanonicalJSON, encoded.Hash
	derived, subscriptions, err := deriveIntegrationLaunch(base, integration, event.Event.Scope)
	require.NoError(t, err)
	require.Len(t, subscriptions, 1)
	other, otherSubscriptions, err := deriveIntegrationLaunch(base, integration, integrationdefinition.Scope{
		Slack: &integrationdefinition.SlackScope{ChannelID: "C456", ThreadTS: "7.8"},
	})
	require.NoError(t, err)
	require.Len(t, otherSubscriptions, 1)
	require.Equal(t, derived.EffectiveDefinitionHash, other.EffectiveDefinitionHash,
		"different conversations reuse the same integration capability config")
	require.NotEqual(t, subscriptions[0].Conversation, otherSubscriptions[0].Conversation)

	var actual agentconfig.Compiled
	require.NoError(t, json.Unmarshal(derived.CompiledDefinition, &actual))
	require.Equal(t, tool, actual.Tools[toolKey])
	require.Equal(t, compiled.InteractionHandlers, actual.InteractionHandlers)
	require.Equal(t, integrationID, actual.Tools[toolcatalog.IntegrationToolName(integration.Name, "read")].IntegrationID)
}

func TestIntegrationLaunchSuppliesProviderCapabilitiesAndReplyContext(t *testing.T) {
	for _, test := range []struct {
		integrationKind integrationdefinition.Kind
		provider        string
		scope           integrationdefinition.Scope
		address         string
	}{
		{
			integrationdefinition.SlackThread, "slack",
			integrationdefinition.Scope{Slack: &integrationdefinition.SlackScope{ChannelID: "C123", ThreadTS: "1.2"}},
			`{"channel_id":"C123","thread_ts":"1.2"}`,
		},
		{
			integrationdefinition.DiscordThread, "discord",
			integrationdefinition.Scope{
				Discord: &integrationdefinition.DiscordScope{GuildID: "100", ChannelID: "300", ThreadID: "500"},
			},
			`{"guild_id":"100","channel_id":"300","thread_id":"500"}`,
		},
		{
			integrationdefinition.GitHubPR, "github",
			integrationdefinition.Scope{
				GitHub: &integrationdefinition.GitHubScope{RepositoryID: 9007199254740993, PullRequest: 42},
			},
			`{"repository_id":9007199254740993,"pull_request":42}`,
		},
	} {
		t.Run(test.provider, func(t *testing.T) {
			_, execution, _, integration, _ := integrationPlannerFixture(t)
			base := execution.profile.CurrentConfig
			integration.Provider, integration.IntegrationKind, integration.Name = test.provider, test.integrationKind, "receiver"
			derived, subscriptions, err := deriveIntegrationLaunch(base, integration, test.scope)
			require.NoError(t, err)
			require.Len(t, subscriptions, 1)
			subscription := subscriptions[0]
			var compiled agentconfig.Compiled
			require.NoError(t, json.Unmarshal(derived.CompiledDefinition, &compiled))
			definition, _ := integrationdefinition.Lookup(integration.IntegrationKind)
			if definition.Provider == integrationdefinition.ProviderGitHub {
				require.Equal(t, &agentconfig.GitCredentialsCompiled{
					Integration: integration.Name, IntegrationID: integration.ID,
				}, compiled.GitCredentials)
			} else {
				require.Nil(t, compiled.GitCredentials)
			}
			for _, operation := range definition.Tools {
				require.Equal(
					t,
					integration.ID,
					compiled.Tools[toolcatalog.IntegrationToolName(integration.Name, operation)].IntegrationID,
				)
			}
			require.Equal(t, integration.ID, subscription.IntegrationID)
			require.JSONEq(t, test.address, string(subscription.Conversation))
			if definition.InteractionHandler != nil {
				require.NotEmpty(t, compiled.InteractionHandlers[integration.Name].IntegrationID)
			} else {
				require.Empty(t, compiled.InteractionHandlers)
			}
			for _, name := range toolcatalog.InteractionHandlerToolNames() {
				tool, exists := compiled.Tools[name]
				require.Equal(t, definition.InteractionHandler != nil, exists)
				if exists {
					require.True(t, tool.Enabled)
					require.Equal(t, toolpermission.ModeAlwaysAllow, tool.Permission.Mode)
				}
			}
			content, err := integrationdefinition.AppendInputContext(integration.Name, test.scope, json.RawMessage(`[{"type":"text","text":"hello"}]`))
			require.NoError(t, err)
			var blocks []struct {
				Text     string
				Metadata map[string]string
			}
			require.NoError(t, json.Unmarshal(content, &blocks))
			require.Equal(t, "hello", blocks[0].Text)
			require.Equal(t, "true", blocks[1].Metadata["omnara_hidden"])
			require.Contains(t, blocks[1].Text, `"integration":"receiver"`)
			require.Contains(t, blocks[1].Text, test.address)
		})
	}
}

func TestGitHubLaunchPreservesExplicitGitCredentials(t *testing.T) {
	_, execution, _, integration, _ := integrationPlannerFixture(t)
	base := execution.profile.CurrentConfig
	integration.Provider = integrationdefinition.ProviderGitHub
	integration.IntegrationKind, integration.Name = integrationdefinition.GitHubPR, "reviews"
	var compiled agentconfig.Compiled
	require.NoError(t, json.Unmarshal(base.CompiledDefinition, &compiled))
	compiled.GitCredentials = &agentconfig.GitCredentialsCompiled{Integration: "checkout", IntegrationID: uuid.New()}
	encoded, err := agentconfig.EncodeCompiled(compiled)
	require.NoError(t, err)
	base.CompiledDefinition, base.EffectiveDefinitionHash = encoded.CanonicalJSON, encoded.Hash
	derived, err := deriveIntegrationLaunchConfig(base, integration)
	require.NoError(t, err)
	var actual agentconfig.Compiled
	require.NoError(t, json.Unmarshal(derived.CompiledDefinition, &actual))
	require.Equal(t, compiled.GitCredentials, actual.GitCredentials)
	require.Equal(t, integration.ID, actual.Tools[toolcatalog.IntegrationToolName(integration.Name, "read")].IntegrationID)

	compiled.GitCredentials.Integration = integration.Name
	encoded, err = agentconfig.EncodeCompiled(compiled)
	require.NoError(t, err)
	base.CompiledDefinition, base.EffectiveDefinitionHash = encoded.CanonicalJSON, encoded.Hash
	_, err = deriveIntegrationLaunchConfig(base, integration)
	require.ErrorIs(t, err, ErrIntegrationLaunchUnavailable,
		"a reused integration name cannot replace the profile's pinned identity")
}

func TestIntegrationLaunchSubscriptionsFollowDefinition(t *testing.T) {
	integrationID := uuid.New()
	scope := integrationdefinition.Scope{Slack: &integrationdefinition.SlackScope{ChannelID: "C123", ThreadTS: "1.2"}}
	definition := integrationdefinition.Definition{
		Provider: integrationdefinition.ProviderSlack,
		Subscription: &integrationdefinition.SubscriptionDefinition{
			Provider: integrationdefinition.ProviderSlack, Events: []string{"message"},
		},
		SubscribeOnLaunch: true,
	}
	subscriptions, err := integrationLaunchSubscriptions(integrationID, definition, scope)
	require.NoError(t, err)
	require.Equal(t, []integrationstore.IntegrationSubscriptionAttachment{{
		IntegrationID: integrationID,
		Conversation:  json.RawMessage(`{"channel_id":"C123","thread_ts":"1.2"}`),
	}}, subscriptions)

	definition.SubscribeOnLaunch = false
	subscriptions, err = integrationLaunchSubscriptions(integrationID, definition, scope)
	require.NoError(t, err)
	require.Empty(t, subscriptions, "exported subscriptions do not imply a launch attachment")
	definition.Subscription = nil
	subscriptions, err = integrationLaunchSubscriptions(integrationID, definition, scope)
	require.NoError(t, err)
	require.Empty(t, subscriptions, "launches need not export any subscription")
	_, err = integrationLaunchSubscriptions(integrationID, definition, integrationdefinition.Scope{})
	require.Error(t, err, "a launch still requires a valid event scope without subscriptions")

	definition.SubscribeOnLaunch = true
	_, err = integrationLaunchSubscriptions(integrationID, definition, scope)
	require.EqualError(t, err, `integration cannot subscribe on launch without a subscription capability`)
}

func TestIntegrationLaunchPreservesInteractionToolOverrides(t *testing.T) {
	_, execution, _, integration, event := integrationPlannerFixture(t)
	base := execution.profile.CurrentConfig
	var compiled agentconfig.Compiled
	require.NoError(t, json.Unmarshal(base.CompiledDefinition, &compiled))
	compiled.InteractionHandlers = map[string]agentconfig.IntegrationCapabilityCompiled{
		integration.Name: {IntegrationID: integration.ID},
	}
	compiled.Tools[toolcatalog.ToolNameListInteractionHandlers] = agentconfig.ToolCompiled{
		Enabled: true, Deferred: true, Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk),
	}
	compiled.Tools[toolcatalog.ToolNameSetInteractionHandler] = agentconfig.ToolCompiled{
		Enabled: false, Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysDeny),
	}
	encoded, err := agentconfig.EncodeCompiled(compiled)
	require.NoError(t, err)
	base.CompiledDefinition, base.EffectiveDefinitionHash = encoded.CanonicalJSON, encoded.Hash
	for _, integrationKind := range []integrationdefinition.Kind{
		integrationdefinition.SlackThread, integrationdefinition.DiscordThread, integrationdefinition.GitHubPR,
	} {
		t.Run(string(integrationKind), func(t *testing.T) {
			integration := integration
			var scope integrationdefinition.Scope
			integration.IntegrationKind = integrationKind
			switch integrationKind {
			case integrationdefinition.SlackThread:
				scope = event.Event.Scope
			case integrationdefinition.DiscordThread:
				integration.ID, integration.Name = uuid.New(), "discord"
				integration.Provider = integrationdefinition.ProviderDiscord
				scope = integrationdefinition.Scope{
					Discord: &integrationdefinition.DiscordScope{GuildID: "100", ChannelID: "300", ThreadID: "500"},
				}
			case integrationdefinition.GitHubPR:
				integration.ID, integration.Name, integration.Provider = uuid.New(), "github", integrationdefinition.ProviderGitHub
				scope = integrationdefinition.Scope{GitHub: &integrationdefinition.GitHubScope{RepositoryID: 123, PullRequest: 42}}
			}
			derived, _, err := deriveIntegrationLaunch(base, integration, scope)
			require.NoError(t, err)
			var actual agentconfig.Compiled
			require.NoError(t, json.Unmarshal(derived.CompiledDefinition, &actual))
			for _, name := range toolcatalog.InteractionHandlerToolNames() {
				require.Equal(t, compiled.Tools[name], actual.Tools[name])
			}
			require.Equal(t, compiled.InteractionHandlers["chat"], actual.InteractionHandlers["chat"])
		})
	}
}

func TestIntegrationPlanLargeSingleMessageFanoutSharesContentAndDeduplicatesAgents(t *testing.T) {
	router, _, integrations, _, event := integrationPlannerFixture(t)
	text := "large-single-message:" + strings.Repeat("<&>\n", 25000)
	content, err := json.Marshal([]map[string]string{{"type": "text", "text": text}})
	require.NoError(t, err)
	event.ContentBlocks = content
	request, err := prepareIntegrationEvent(event, integrations.integrationSetup)
	require.NoError(t, err)
	// The planner accepts the full routing budget, not an invented aggregate 64 cap.
	for range 8 * 16 {
		id := uuid.New()
		request.candidates.Subscriptions = append(request.candidates.Subscriptions,
			integrationstore.IntegrationSubscriptionRecord{AgentID: id, Address: request.address},
			integrationstore.IntegrationSubscriptionRecord{AgentID: id, Address: request.scopes[1]})
	}
	plan, err := router.buildIntegrationPlan(t.Context(), integrations.receipt, integrations.integrationSetup, request)
	require.NoError(t, err)
	require.Len(t, plan.Recipients, 128)
	raw, err := json.Marshal(plan)
	require.NoError(t, err)
	require.Equal(t, 1, bytes.Count(raw, []byte("large-single-message:")))
	require.Less(t, len(raw), len(event.ContentBlocks)+128*1024)
	require.Less(t, len(raw), integrationstore.IntegrationInboxMaxPlanBytes)
	for _, recipient := range plan.Recipients {
		require.Len(t, recipient.Subscription.Alternatives, 2)
		recipient, err := json.Marshal(recipient)
		require.NoError(t, err)
		require.NotContains(t, string(recipient), "content_blocks")
		require.NotContains(t, string(recipient), "metadata")
	}
}

func TestIntegrationPlanRecipientAllowanceCoversSupportedFacts(t *testing.T) {
	// Routing addresses from supported providers are canonical ASCII IDs, so their
	// schema byte maxima incur no JSON escape expansion. Files contribute UUIDs only.
	recipient := IntegrationInboxRecipient{
		AgentID: uuid.Must(uuid.NewV7()), Subscription: &executionstore.InboxSubscriptionAuthority{},
	}
	for range 8 {
		recipient.Subscription.Alternatives = append(recipient.Subscription.Alternatives,
			integrationstore.ConversationAddress{Kind: strings.Repeat("x", 128), Ref: strings.Repeat("C", 2048)},
		)
	}
	for range 20 {
		recipient.ArtifactIDs = append(recipient.ArtifactIDs, uuid.Must(uuid.NewV7()))
	}
	address := integrationstore.ConversationAddress{Kind: "thread", Ref: strings.Repeat("C", 2048)}
	recipient.LaunchClaim = &integrationstore.InboxLaunchClaim{
		IntegrationID: uuid.New(), Address: address, LaunchKey: "scheduled",
	}
	recipient.Launch = &executionstore.InboxLaunchPlan{
		ProfileID: uuid.New(), AgentConfigID: uuid.New(), DerivedBaseConfigID: uuid.New(),
		IdempotencyKey: strings.Repeat("x", 128),
		Subscriptions: []integrationstore.IntegrationSubscriptionAttachment{{
			IntegrationID: uuid.New(), Conversation: json.RawMessage(`{"channel_id":"` + strings.Repeat("C", 2048) + `"}`),
		}},
	}
	// This combines even the mutually exclusive launch/subscription facts to bound
	// either real recipient; indentation overestimates jsonb's separator spaces.
	raw, err := json.MarshalIndent(recipient, "", " ")
	require.NoError(t, err)
	require.Less(t, len(raw), 32*1024)
	t.Logf("conservative recipient envelope: %d bytes of 32768", len(raw))
}
