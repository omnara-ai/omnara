package integration

import (
	"context"
	"encoding/json"
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
	integrationSetup integrationstore.ProjectIntegrationRecord
	receipt          integrationstore.IntegrationInboxRecord
}

func (s *integrationPlanStore) GetProjectIntegrationByID(
	context.Context,
	uuid.UUID,
) (integrationstore.ProjectIntegrationRecord, error) {
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
	integrationstore.ProjectIntegrationRecord,
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
				IntegrationType: integrationdefinition.SlackThread,
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
		integrationSetup: integrationstore.ProjectIntegrationRecord{
			ID:   integrationID,
			Name: "chat", IntegrationType: integrationdefinition.SlackThread,
			OrgID:             org,
			ProjectID:         project,
			Provider:          "slack",
			ProviderTenantID:  "T123",
			State:             integrationstore.ProjectIntegrationStateActive,
			InstalledByUserID: uuid.New(),
		},
	}
	integrations.receipt = integrationstore.IntegrationInboxRecord{
		ID:            uuid.New(),
		ProjectID:     project,
		IntegrationID: integrations.integrationSetup.ID,
	}
	integration := integrations.integrationSetup
	integration.Settings = integrationstore.ProjectIntegrationSettings{
		Launcher: &integrationstore.IntegrationLauncher{
			Trigger:   "mention",
			ScopeKind: "workspace",
			ScopeRef:  "T123",
			Slots: []integrationstore.IntegrationLaunchSlot{
				{Key: "a", AgentProfileID: &execution.profile.ID},
				{Key: "b", AgentProfileID: &execution.profile.ID},
			},
		},
	}
	event := IntegrationEvent{
		Event: integrationdefinition.Event{
			Scope:     integrationdefinition.Scope{Slack: &integrationdefinition.SlackScope{ChannelID: "C123", ThreadTS: "1.2"}},
			Kind:      "message",
			Mentioned: true,
		},
		SemanticKey:   "message:1",
		ContentBlocks: json.RawMessage(`[{"type":"text","text":"hello"}]`),
		Actor:         integrationTestActor(t, integration.ID, "U123"),
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
	event.Files = []IntegrationPlannedFile{{ArtifactID: oldID, ProviderFileID: "F123"}}
	requests, err := prepareIntegrationEvents([]IntegrationEvent{event}, integrations.integrationSetup)
	require.NoError(t, err)
	requests[0].candidates.Launcher = &integration
	applyTestIntegrationLaunchPolicy(t, integrations.receipt, integrations.integrationSetup, requests)
	plan, err := router.buildIntegrationPlan(t.Context(), integrations.receipt, integrations.integrationSetup, requests)
	require.NoError(t, err)
	require.Len(t, plan, 2)
	require.Equal(t, 1, execution.reads)
	agents, artifacts := map[uuid.UUID]bool{}, map[uuid.UUID]bool{}
	for _, slot := range plan {
		require.Equal(t, uuid.Version(7), slot.AgentID.Version())
		require.False(t, agents[slot.AgentID])
		agents[slot.AgentID] = true
		require.Equal(t, execution.profile.CurrentConfig.ID, slot.Launch.DerivedBaseConfigID)
		require.Empty(t, execution.configs[slot.Launch.AgentConfigID].Source)
		require.Nil(t, slot.Input)
		require.Len(t, slot.Files, 1)
		require.False(t, artifacts[slot.Files[0].ArtifactID])
		artifacts[slot.Files[0].ArtifactID] = true
		require.NotEqual(t, oldID, slot.Files[0].ArtifactID)
		var compiled agentconfig.Compiled
		require.NoError(t, json.Unmarshal(execution.configs[slot.Launch.AgentConfigID].CompiledDefinition, &compiled))
		require.Equal(t, "Pinned original", compiled.Instruction)
		require.Equal(t, base.Model, compiled.Model)
		require.Equal(t, base.EventWebhook, compiled.EventWebhook)
		require.False(t, compiled.Tools[toolcatalog.IntegrationToolName("chat", "post_message")].Enabled)
		require.Len(t, slot.Launch.Subscriptions, 1)
		subscription := slot.Launch.Subscriptions[0]
		require.Equal(t, integration.ID, subscription.IntegrationID)
		require.JSONEq(t, `{"channel_id":"C123","thread_ts":"1.2"}`, string(subscription.Conversation))
		require.Equal(
			t,
			integration.ID,
			compiled.Tools[toolcatalog.IntegrationToolName(integration.Name, "read")].IntegrationID,
		)
		require.Equal(t, integration.ID, compiled.InteractionHandlers[integration.Name].IntegrationID)
		raw, err := json.Marshal(slot)
		require.NoError(t, err)
		var kernel executionstore.InboxLaunchSlot
		require.NoError(t, json.Unmarshal(raw, &kernel))
		require.Equal(t, *slot.Selection, kernel.Selection)
		require.Equal(t, slot.AgentID, kernel.AgentID)
		require.Equal(t, slot.Launch.Subscriptions, kernel.Launch.Subscriptions)
		require.Equal(t, execution.configs[slot.Launch.AgentConfigID], execution.configs[kernel.Launch.AgentConfigID])
		contract, err := agentconfig.RuntimeContractFromCompiled(
			execution.configs[kernel.Launch.AgentConfigID].CompiledDefinition,
			execution.configs[kernel.Launch.AgentConfigID].EffectiveDefinitionHash,
		)
		require.NoError(t, err)
		require.Equal(t, []uuid.UUID{integration.ID}, contract.ReferencedIntegrationIDs())
	}
	integrations.receipt.Plan, err = json.Marshal(plan)
	require.NoError(t, err)
	integrations.integrationSetup.State = integrationstore.ProjectIntegrationStateDisconnected
	got, err := router.Freeze(t.Context(), integrations.receipt.Lease(), nil)
	require.NoError(t, err)
	require.Equal(t, plan, got)
	got, err = freezeTestIntegrationEvents(t.Context(), router, integrations.receipt.Lease(), nil)
	require.NoError(t, err, "the policy helper must also leave frozen retries untouched")
	require.Equal(t, plan, got)
}

func TestIntegrationPlanExistingTriggersOverlapAndRetiredSelection(t *testing.T) {
	router, execution, integrations, integration, event := integrationPlannerFixture(t)
	agent := uuid.New()
	integration.Settings.Launcher.Slots = append(
		integration.Settings.Launcher.Slots,
		integrationstore.IntegrationLaunchSlot{Key: "trigger", AgentID: &agent},
	)
	requests, err := prepareIntegrationEvents([]IntegrationEvent{event}, integrations.integrationSetup)
	require.NoError(t, err)
	requests[0].candidates = integrationstore.IntegrationRoutingCandidates{
		Launcher: &integration,
		Subscriptions: []integrationstore.IntegrationSubscriptionRecord{
			{
				AgentID: agent,
				Address: integrationstore.ConversationAddress{Kind: "channel", Ref: "C123"},
			},
		},
		Selections: []integrationstore.IntegrationTargetRecord{
			{IntegrationID: integration.ID, SelectionSlot: "removed-old-slot"},
		},
	}
	applyTestIntegrationLaunchPolicy(t, integrations.receipt, integrations.integrationSetup, requests)
	plan, err := router.buildIntegrationPlan(t.Context(), integrations.receipt, integrations.integrationSetup, requests)
	require.NoError(t, err)
	require.Len(t, plan, 1)
	require.Zero(t, execution.reads)
	for _, slot := range plan {
		require.Nil(t, slot.Selection)
		require.Nil(t, slot.Launch)
		require.Nil(t, slot.Subscription, "explicit trigger is independent of subscription revocation")
		require.Equal(t, agent, slot.AgentID)
	}
	requests[0].candidates.Selections = nil
	requests[0].candidates.Subscriptions[0].Address = requests[0].address
	applyTestIntegrationLaunchPolicy(t, integrations.receipt, integrations.integrationSetup, requests)
	plan, err = router.buildIntegrationPlan(t.Context(), integrations.receipt, integrations.integrationSetup, requests)
	require.NoError(t, err)
	require.Len(t, plan, 1)
	requests[0].candidates.Subscriptions[0].Address = integrationstore.ConversationAddress{Kind: "channel", Ref: "C123"}
	applyTestIntegrationLaunchPolicy(t, integrations.receipt, integrations.integrationSetup, requests)
	plan, err = router.buildIntegrationPlan(t.Context(), integrations.receipt, integrations.integrationSetup, requests)
	require.NoError(t, err)
	require.Len(t, plan, 3)
}

func TestIntegrationPlanExpansionContinuesLauncherRuntimeSubscription(t *testing.T) {
	router, _, integrations, integration, first := integrationPlannerFixture(t)
	second := first
	second.SemanticKey = "message:0"
	second.Event.Mentioned = false
	requests, err := prepareIntegrationEvents([]IntegrationEvent{first, second}, integrations.integrationSetup)
	require.NoError(t, err)
	for i := range requests {
		requests[i].candidates.Launcher = &integration
	}
	applyTestIntegrationLaunchPolicy(t, integrations.receipt, integrations.integrationSetup, requests)
	plan, err := router.buildIntegrationPlan(t.Context(), integrations.receipt, integrations.integrationSetup, requests)
	require.NoError(t, err)
	require.Len(t, plan, 4)
	var launches, inputs int
	for _, slot := range plan {
		if slot.Launch != nil {
			launches++
			require.Zero(t, slot.EventOrder)
			require.Equal(t, first.SemanticKey, slot.Launch.InitialInput.SemanticEventKey)
		} else {
			inputs++
			require.Equal(t, 1, slot.EventOrder)
			require.NotNil(t, slot.Subscription)
			require.Equal(t, second.SemanticKey, slot.Input.IdempotencyKey)
		}
	}
	require.Equal(t, 2, launches)
	require.Equal(t, 2, inputs)
}

func TestIntegrationPlanDiscordThreadRequiresExactSubscription(t *testing.T) {
	router, _, integrations, _, event := integrationPlannerFixture(t)
	integrations.integrationSetup.Provider = "discord"
	integrations.integrationSetup.ProviderTenantID = "11"
	event.Event.Scope = integrationdefinition.Scope{
		Discord: &integrationdefinition.DiscordScope{GuildID: "100", ChannelID: "300", ThreadID: "500"},
	}
	event.Event.Mentioned = false
	requests, err := prepareIntegrationEvents([]IntegrationEvent{event}, integrations.integrationSetup)
	require.NoError(t, err)
	parent, exact := uuid.New(), uuid.New()
	requests[0].candidates.Subscriptions = []integrationstore.IntegrationSubscriptionRecord{
		{
			AgentID: parent,
			Address: integrationstore.ConversationAddress{Kind: "channel", Ref: "300"},
		},
		{
			AgentID: exact,
			Address: integrationstore.ConversationAddress{Kind: "thread", Ref: "300:500"},
		},
	}
	applyTestIntegrationLaunchPolicy(t, integrations.receipt, integrations.integrationSetup, requests)
	plan, err := router.buildIntegrationPlan(t.Context(), integrations.receipt, integrations.integrationSetup, requests)
	require.NoError(t, err)
	require.Len(t, plan, 1)
	for _, slot := range plan {
		require.Equal(t, exact, slot.AgentID)
	}
	requests[0].candidates.Subscriptions = requests[0].candidates.Subscriptions[:1]
	applyTestIntegrationLaunchPolicy(t, integrations.receipt, integrations.integrationSetup, requests)
	plan, err = router.buildIntegrationPlan(t.Context(), integrations.receipt, integrations.integrationSetup, requests)
	require.NoError(t, err)
	require.Empty(t, plan, "parent authority must not subscribe every thread")
}

func TestIntegrationPlanLaunchesOnlyExplicitIntents(t *testing.T) {
	router, execution, integrations, integration, event := integrationPlannerFixture(t)
	requests, err := prepareIntegrationEvents([]IntegrationEvent{event}, integrations.integrationSetup)
	require.NoError(t, err)
	requests[0].candidates.Launcher = &integration
	plan, err := router.buildIntegrationPlan(t.Context(), integrations.receipt, integrations.integrationSetup, requests)
	require.NoError(t, err)
	require.Empty(t, plan, "matching saved setups do not authorize an implicit launch")
	require.Zero(t, execution.reads)

	requests[0].event.Event.Mentioned = false
	requests[0].event.Directed = true
	requests[0].event.Launches = []IntegrationLaunchIntent{
		{IntegrationID: integration.ID, Slot: "b", ProfileID: execution.profile.ID},
	}
	requests[0].candidates.Subscriptions = []integrationstore.IntegrationSubscriptionRecord{
		{AgentID: uuid.New(), Address: requests[0].address},
	}
	plan, err = router.buildIntegrationPlan(t.Context(), integrations.receipt, integrations.integrationSetup, requests)
	require.NoError(t, err)
	require.Len(t, plan, 1)
	for _, slot := range plan {
		require.NotNil(t, slot.Launch)
		require.Equal(t, "b", slot.Selection.Slot)
	}
}

func TestIntegrationPlanRejectsUnavailableLaunchIntents(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*integrationEventCandidates)
	}{
		{"absent integration", func(r *integrationEventCandidates) { r.candidates.Launcher = nil }},
		{"disabled integration", func(r *integrationEventCandidates) {
			r.candidates.Launcher.State = integrationstore.ProjectIntegrationStateDisconnected
		}},
		{"wrong project", func(r *integrationEventCandidates) { r.candidates.Launcher.ProjectID = uuid.New() }},
		{"removed launcher", func(r *integrationEventCandidates) { r.candidates.Launcher.Settings.Launcher = nil }},
		{"removed slot", func(r *integrationEventCandidates) { r.candidates.Launcher.Settings.Launcher.Slots = nil }},
		{"retargeted profile", func(r *integrationEventCandidates) {
			other := uuid.New()
			r.candidates.Launcher.Settings.Launcher.Slots[0].AgentProfileID = &other
		}},
		{"changed to fixed agent", func(r *integrationEventCandidates) {
			other := uuid.New()
			r.candidates.Launcher.Settings.Launcher.Slots[0] = integrationstore.IntegrationLaunchSlot{
				Key: "a", AgentID: &other,
			}
		}},
		{"missing expected recipient", func(r *integrationEventCandidates) { r.event.Launches[0].ProfileID = uuid.Nil }},
		{"ambiguous recipient", func(r *integrationEventCandidates) { r.event.Launches[0].AgentID = uuid.New() }},
		{"retargeted fixed agent", func(r *integrationEventCandidates) {
			actual, expected := uuid.New(), uuid.New()
			r.candidates.Launcher.Settings.Launcher.Slots[0] = integrationstore.IntegrationLaunchSlot{
				Key: "a", AgentID: &actual,
			}
			r.event.Launches[0].ProfileID, r.event.Launches[0].AgentID = uuid.Nil, expected
		}},
		{"retired selection", func(r *integrationEventCandidates) {
			now := time.Now()
			r.candidates.Selections = []integrationstore.IntegrationTargetRecord{{
				IntegrationID: r.event.Launches[0].IntegrationID, SelectionSlot: "a", AgentID: uuid.New(), DeletedAt: &now,
			}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			router, execution, integrations, integration, event := integrationPlannerFixture(t)
			event.Launches = []IntegrationLaunchIntent{
				{IntegrationID: integration.ID, Slot: "a", ProfileID: execution.profile.ID},
			}
			requests, err := prepareIntegrationEvents([]IntegrationEvent{event}, integrations.integrationSetup)
			require.NoError(t, err)
			requests[0].candidates.Launcher = &integration
			test.change(&requests[0])
			plan, err := router.buildIntegrationPlan(t.Context(), integrations.receipt, integrations.integrationSetup, requests)
			require.ErrorIs(t, err, ErrIntegrationLaunchUnavailable)
			require.Empty(t, plan)
			require.Zero(t, execution.reads, "stale choices must fail before profile derivation")
		})
	}
}

func TestIntegrationPlanDirectedExpansionReusesSelectedIdentity(t *testing.T) {
	for _, settled := range []bool{false, true} {
		name := "planned in expansion"
		if settled {
			name = "settled without subscription"
		}
		t.Run(name, func(t *testing.T) {
			router, execution, integrations, integration, first := integrationPlannerFixture(t)
			first.Directed = true
			first.Launches = []IntegrationLaunchIntent{
				{IntegrationID: integration.ID, Slot: "a", ProfileID: execution.profile.ID},
				{IntegrationID: integration.ID, Slot: "b", ProfileID: execution.profile.ID},
			}
			second := first
			second.SemanticKey = "message:files"
			second.Event.Mentioned = false
			second.Launches = second.Launches[:1]
			oldArtifact := uuid.Must(uuid.NewV7())
			second.ContentBlocks = json.RawMessage(
				`[{"type":"media_ref","artifact_id":"` + oldArtifact.String() + `"}]`,
			)
			second.Files = []IntegrationPlannedFile{{ArtifactID: oldArtifact, ProviderFileID: "F123"}}
			first.Launches = append(first.Launches, first.Launches[0])
			requests, err := prepareIntegrationEvents([]IntegrationEvent{first, second}, integrations.integrationSetup)
			require.NoError(t, err)
			agents := map[string]uuid.UUID{"a": uuid.New(), "b": uuid.New()}
			for i := range requests {
				requests[i].candidates.Launcher = &integration
				requests[i].candidates.Subscriptions = []integrationstore.IntegrationSubscriptionRecord{
					{AgentID: uuid.New(), Address: requests[i].address},
				}
				if settled {
					for slot, agent := range agents {
						requests[i].candidates.Selections = append(requests[i].candidates.Selections,
							integrationstore.IntegrationTargetRecord{
								IntegrationID: integration.ID,
								SelectionSlot: slot,
								AgentID:       agent,
							})
					}
				}
			}
			plan, err := router.buildIntegrationPlan(t.Context(), integrations.receipt, integrations.integrationSetup, requests)
			require.NoError(t, err)
			require.Len(t, plan, 3, "two chosen slots, then a file only for the chosen original recipient")
			launches := 0
			for _, slot := range plan {
				if slot.Launch != nil {
					launches++
					require.Zero(t, slot.EventOrder)
					agents[slot.Selection.Slot] = slot.AgentID
				}
			}
			if settled {
				require.Zero(t, launches)
				require.Zero(t, execution.reads)
			} else {
				require.Equal(t, 2, launches)
				require.Equal(t, 1, execution.reads)
			}
			inputs := 0
			for _, slot := range plan {
				if slot.Input == nil {
					continue
				}
				require.Nil(t, slot.Selection)
				require.Nil(t, slot.Subscription, "explicit original-source delivery does not depend on a subscription")
				if slot.EventOrder == 1 {
					inputs++
					require.Equal(t, agents["a"], slot.AgentID)
					require.Equal(t, second.SemanticKey, slot.Input.IdempotencyKey)
					require.Len(t, slot.Files, 1)
					require.NotEqual(t, oldArtifact, slot.Files[0].ArtifactID)
				} else {
					require.Contains(t, []uuid.UUID{agents["a"], agents["b"]}, slot.AgentID)
					require.Equal(t, first.SemanticKey, slot.Input.IdempotencyKey)
				}
			}
			require.Equal(t, 1, inputs, "directed files must skip even subscriptions planned earlier in this expansion")
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
		integrationType integrationdefinition.Type
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
			integration.Provider, integration.IntegrationType, integration.Name = test.provider, test.integrationType, "receiver"
			derived, subscriptions, err := deriveIntegrationLaunch(base, integration, test.scope)
			require.NoError(t, err)
			require.Len(t, subscriptions, 1)
			subscription := subscriptions[0]
			var compiled agentconfig.Compiled
			require.NoError(t, json.Unmarshal(derived.CompiledDefinition, &compiled))
			definition, _ := integrationdefinition.Lookup(integration.IntegrationType)
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
	for _, integrationType := range []integrationdefinition.Type{
		integrationdefinition.SlackThread, integrationdefinition.DiscordThread, integrationdefinition.GitHubPR,
	} {
		t.Run(string(integrationType), func(t *testing.T) {
			integration := integration
			var scope integrationdefinition.Scope
			integration.IntegrationType = integrationType
			switch integrationType {
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

func TestIntegrationPlanFrozenSubscriptionsRouteLaterMessagesAndMedia(t *testing.T) {
	for _, provider := range []string{
		integrationdefinition.ProviderSlack, integrationdefinition.ProviderDiscord, integrationdefinition.ProviderGitHub,
	} {
		t.Run(provider, func(t *testing.T) {
			router, execution, integrations, integration, first := integrationPlannerFixture(t)
			integration.Name = "receiver"
			integration.Settings.Launcher.Slots = integration.Settings.Launcher.Slots[:1]
			kind := "message"
			otherScope := integrationdefinition.Scope{
				Slack: &integrationdefinition.SlackScope{ChannelID: "C123", ThreadTS: "1.3"},
			}
			switch provider {
			case integrationdefinition.ProviderDiscord:
				integration.Provider = provider
				integration.IntegrationType, integration.ProviderTenantID = integrationdefinition.DiscordThread, "11"
				first.Event.Scope = integrationdefinition.Scope{
					Discord: &integrationdefinition.DiscordScope{GuildID: "100", ChannelID: "300", ThreadID: "500"},
				}
				otherScope = integrationdefinition.Scope{
					Discord: &integrationdefinition.DiscordScope{GuildID: "100", ChannelID: "300", ThreadID: "501"},
				}
			case integrationdefinition.ProviderGitHub:
				integration.Provider, integration.IntegrationType = provider, integrationdefinition.GitHubPR
				integration.ProviderTenantID, integration.ProviderAccountRef = "11", "22"
				first.Event.Scope = integrationdefinition.Scope{
					GitHub: &integrationdefinition.GitHubScope{RepositoryID: 123, PullRequest: 42},
				}
				otherScope = integrationdefinition.Scope{
					GitHub: &integrationdefinition.GitHubScope{RepositoryID: 123, PullRequest: 43},
				}
				first.Event.Kind = "pull_request_opened"
				kind = "commit"
			}
			integrations.integrationSetup = integration
			first.Actor = integrationTestActor(t, integration.ID, first.Actor.ProviderUserID)
			first.SemanticKey = "z:launch"
			first.Launches = []IntegrationLaunchIntent{
				{IntegrationID: integration.ID, Slot: "a", ProfileID: execution.profile.ID},
			}
			reply := first
			reply.Launches, reply.Event.Mentioned, reply.Event.Kind = nil, false, kind
			reply.SemanticKey = "b:reply"
			before := reply
			before.SemanticKey = "c:before-launch"
			media := reply
			media.SemanticKey = "a:media"
			placeholder := uuid.New()
			media.ContentBlocks = json.RawMessage(`[{"type":"media_ref","artifact_id":"` + placeholder.String() + `"}]`)
			media.Files = []IntegrationPlannedFile{{ArtifactID: placeholder, ProviderFileID: "F123"}}
			other := reply
			other.SemanticKey, other.Event.Scope = "other:conversation", otherScope
			events := []IntegrationEvent{before, first, reply, media, other}
			if provider == integrationdefinition.ProviderGitHub {
				excluded := reply
				excluded.SemanticKey, excluded.Event.Kind = "excluded:event", "pull_request_opened"
				events = append(events, excluded)
			}
			requests, err := prepareIntegrationEvents(events, integration)
			require.NoError(t, err)
			for i := range requests {
				requests[i].candidates.Launcher = &integration
			}
			plan, err := router.buildIntegrationPlan(t.Context(), integrations.receipt, integration, requests)
			require.NoError(t, err)
			require.Len(t, plan, 3, "only the launch and later matching conversation/events receive input")
			var agentID uuid.UUID
			for _, slot := range plan {
				if slot.Launch == nil {
					continue
				}
				agentID = slot.AgentID
				require.Equal(t, 1, slot.EventOrder)
				require.Len(t, slot.Launch.Subscriptions, 1)
				subscription := slot.Launch.Subscriptions[0]
				require.Equal(t, integration.ID, subscription.IntegrationID)
				conversation, err := first.Event.Scope.ConversationJSON()
				require.NoError(t, err)
				require.JSONEq(t, string(conversation), string(subscription.Conversation))
			}
			require.NotEqual(t, uuid.Nil, agentID)
			addressKind, addressRef, err := first.Event.Scope.Conversation()
			require.NoError(t, err)
			for _, slot := range plan {
				if slot.Input == nil {
					continue
				}
				require.Equal(t, agentID, slot.AgentID)
				require.Equal(t, &executionstore.InboxSubscriptionAuthority{
					Alternatives: []integrationstore.ConversationAddress{{Kind: addressKind, Ref: addressRef}},
				}, slot.Subscription)
				if slot.EventOrder == 3 {
					require.Len(t, slot.Files, 1)
					require.NotEqual(t, placeholder, slot.Files[0].ArtifactID)
					require.Equal(t, "F123", slot.Files[0].ProviderFileID)
				} else {
					require.Equal(t, 2, slot.EventOrder)
				}
			}
			integrations.receipt.Plan, err = json.Marshal(plan)
			require.NoError(t, err)
			execution.profile.CurrentConfig = executionstore.AgentConfigRecord{}
			integrations.integrationSetup.IntegrationType = "no-longer-available"
			replayed, err := router.Freeze(t.Context(), integrations.receipt.Lease(), []IntegrationEvent{other})
			require.NoError(t, err)
			replayedJSON, err := json.Marshal(replayed)
			require.NoError(t, err)
			require.JSONEq(t, string(integrations.receipt.Plan), string(replayedJSON))
		})
	}
}
