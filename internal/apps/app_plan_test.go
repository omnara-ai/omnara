package apps

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/appstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

type appPlanExecution struct {
	AppExecutionStore
	profile executionstore.AgentProfileRecord
	reads   int
	configs map[uuid.UUID]executionstore.AgentConfigRecord
}

func (s *appPlanExecution) CreateAgentConfig(
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

func (s *appPlanExecution) GetAgentInProject(
	_ context.Context, projectID, id uuid.UUID,
) (executionstore.AgentRecord, error) {
	return executionstore.AgentRecord{ID: id, ProjectID: projectID, AgentProfileID: s.profile.ID}, nil
}

func (s *appPlanExecution) GetAgentProfile(
	context.Context,
	uuid.UUID,
	uuid.UUID,
) (executionstore.AgentProfileRecord, error) {
	s.reads++
	return s.profile, nil
}

type appPlanStore struct {
	AppRoutingStore
	appSetup appstore.ProjectAppRecord
	receipt  appstore.AppInboxRecord
}

func (s *appPlanStore) GetProjectAppByID(
	context.Context,
	uuid.UUID,
) (appstore.ProjectAppRecord, error) {
	return s.appSetup, nil
}

func (s *appPlanStore) GetAppInbox(
	context.Context,
	uuid.UUID,
	uuid.UUID,
) (appstore.AppInboxRecord, error) {
	return s.receipt, nil
}

func appPlannerFixture(
	t *testing.T,
) (*AppRouter, *appPlanExecution, *appPlanStore, appstore.ProjectAppRecord, AppEvent) {
	t.Helper()
	project, org, appID := uuid.New(), uuid.New(), uuid.New()
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
			toolcatalog.AppToolName("chat", "post_message"): {Enabled: &disabled},
		},
	}
	raw, err := json.Marshal(source)
	require.NoError(t, err)
	compiled, err := agentconfig.Compile(agentconfig.SourceFormatJSON, raw, agentconfig.CompileOptions{
		ResolveModelSelection: func(string, string) (agentconfig.ResolvedModelSelection, error) {
			return agentconfig.ResolvedModelSelection{ConfiguredModelID: modelID}, nil
		},
		ResolveAppName: func(string) (agentconfig.AppResolution, error) {
			return agentconfig.AppResolution{AppID: appID, AppType: appdefinition.SlackThread}, nil
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
	execution := &appPlanExecution{
		profile: executionstore.AgentProfileRecord{
			ID:              uuid.New(),
			ProjectID:       project,
			CurrentConfigID: base.ID,
			CurrentConfig:   base,
		},
	}
	apps := &appPlanStore{
		appSetup: appstore.ProjectAppRecord{
			ID:   appID,
			Name: "chat", AppType: appdefinition.SlackThread,
			OrgID:             org,
			ProjectID:         project,
			Provider:          "slack",
			ProviderTenantID:  "T123",
			State:             appstore.ProjectAppStateActive,
			InstalledByUserID: uuid.New(),
		},
	}
	apps.receipt = appstore.AppInboxRecord{
		ID:        uuid.New(),
		ProjectID: project,
		AppID:     apps.appSetup.ID,
	}
	app := apps.appSetup
	app.Settings = appstore.ProjectAppSettings{
		Launcher: &appstore.AppLauncher{
			Trigger:   "mention",
			ScopeKind: "workspace",
			ScopeRef:  "T123",
			Slots: []appstore.AppLaunchSlot{
				{Key: "a", AgentProfileID: &execution.profile.ID},
				{Key: "b", AgentProfileID: &execution.profile.ID},
			},
		},
	}
	event := AppEvent{
		Event: appdefinition.Event{
			Scope:     appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: "C123", ThreadTS: "1.2"}},
			Kind:      "message",
			Mentioned: true,
		},
		SemanticKey:   "message:1",
		ContentBlocks: json.RawMessage(`[{"type":"text","text":"hello"}]`),
		Actor:         appTestActor(t, app.ID, "U123"),
	}
	return NewAppRouter(execution, apps), execution, apps, app, event
}

func TestAppPlanPinsFullProfileMembershipAndCompiledPolicy(t *testing.T) {
	router, execution, apps, app, event := appPlannerFixture(t)
	var base agentconfig.Compiled
	require.NoError(t, json.Unmarshal(execution.profile.CurrentConfig.CompiledDefinition, &base))
	oldID := uuid.Must(uuid.NewV7())
	event.ContentBlocks = json.RawMessage(
		`[{"type":"text","text":"review"},{"type":"media_ref","artifact_id":"` + oldID.String() + `"}]`,
	)
	event.Files = []AppPlannedFile{{ArtifactID: oldID, ProviderFileID: "F123"}}
	requests, err := prepareAppEvents([]AppEvent{event}, apps.appSetup)
	require.NoError(t, err)
	requests[0].candidates.Launcher = &app
	applyTestAppLaunchPolicy(t, apps.receipt, apps.appSetup, requests)
	plan, err := router.buildAppPlan(t.Context(), apps.receipt, apps.appSetup, requests)
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
		require.False(t, compiled.Tools[toolcatalog.AppToolName("chat", "post_message")].Enabled)
		require.Len(t, slot.Launch.Subscriptions, 1)
		subscription := slot.Launch.Subscriptions[0]
		require.Equal(t, app.ID, subscription.AppID)
		require.Equal(t, "thread_messages", subscription.Type)
		require.Equal(t, []string{"message"}, subscription.Events)
		require.JSONEq(t, `{"channel_id":"C123","thread_ts":"1.2"}`, string(subscription.Conversation))
		require.Equal(t, app.ID, compiled.Tools[toolcatalog.AppToolName(app.Name, "read")].AppID)
		require.Equal(t, app.ID, compiled.InteractionHandlers[app.Name].AppID)
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
		require.Equal(t, []uuid.UUID{app.ID}, contract.ReferencedAppIDs())
	}
	apps.receipt.Plan, err = json.Marshal(plan)
	require.NoError(t, err)
	apps.appSetup.State = appstore.ProjectAppStateDisconnected
	got, err := router.Freeze(t.Context(), apps.receipt.Lease(), nil)
	require.NoError(t, err)
	require.Equal(t, plan, got)
	got, err = freezeTestAppEvents(t.Context(), router, apps.receipt.Lease(), nil)
	require.NoError(t, err, "the policy helper must also leave frozen retries untouched")
	require.Equal(t, plan, got)
}

func TestAppPlanExistingTriggersOverlapAndRetiredSelection(t *testing.T) {
	router, execution, apps, app, event := appPlannerFixture(t)
	agent := uuid.New()
	app.Settings.Launcher.Slots = append(
		app.Settings.Launcher.Slots,
		appstore.AppLaunchSlot{Key: "trigger", AgentID: &agent},
	)
	requests, err := prepareAppEvents([]AppEvent{event}, apps.appSetup)
	require.NoError(t, err)
	requests[0].candidates = appstore.AppRoutingCandidates{
		Launcher: &app,
		Subscriptions: []appstore.AppSubscriptionRecord{
			{
				AgentID: agent,
				Type:    "thread_messages",
				Address: appstore.ConversationAddress{Kind: "channel", Ref: "C123"},
			},
		},
		Selections: []appstore.AppTargetRecord{{AppID: app.ID, SelectionSlot: "removed-old-slot"}},
	}
	applyTestAppLaunchPolicy(t, apps.receipt, apps.appSetup, requests)
	plan, err := router.buildAppPlan(t.Context(), apps.receipt, apps.appSetup, requests)
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
	applyTestAppLaunchPolicy(t, apps.receipt, apps.appSetup, requests)
	plan, err = router.buildAppPlan(t.Context(), apps.receipt, apps.appSetup, requests)
	require.NoError(t, err)
	require.Len(t, plan, 1)
	requests[0].candidates.Subscriptions[0].Address = appstore.ConversationAddress{Kind: "channel", Ref: "C123"}
	applyTestAppLaunchPolicy(t, apps.receipt, apps.appSetup, requests)
	plan, err = router.buildAppPlan(t.Context(), apps.receipt, apps.appSetup, requests)
	require.NoError(t, err)
	require.Len(t, plan, 3)
}

func TestAppPlanExpansionContinuesLauncherRuntimeSubscription(t *testing.T) {
	router, _, apps, app, first := appPlannerFixture(t)
	second := first
	second.SemanticKey = "message:0"
	second.Event.Mentioned = false
	requests, err := prepareAppEvents([]AppEvent{first, second}, apps.appSetup)
	require.NoError(t, err)
	for i := range requests {
		requests[i].candidates.Launcher = &app
	}
	applyTestAppLaunchPolicy(t, apps.receipt, apps.appSetup, requests)
	plan, err := router.buildAppPlan(t.Context(), apps.receipt, apps.appSetup, requests)
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

func TestAppPlanDiscordThreadRequiresExactSubscription(t *testing.T) {
	router, _, apps, _, event := appPlannerFixture(t)
	apps.appSetup.Provider = "discord"
	apps.appSetup.ProviderTenantID = "11"
	event.Event.Scope = appdefinition.Scope{
		Discord: &appdefinition.DiscordScope{GuildID: "100", ChannelID: "300", ThreadID: "500"},
	}
	event.Event.Mentioned = false
	requests, err := prepareAppEvents([]AppEvent{event}, apps.appSetup)
	require.NoError(t, err)
	parent, exact := uuid.New(), uuid.New()
	requests[0].candidates.Subscriptions = []appstore.AppSubscriptionRecord{
		{
			AgentID: parent,
			Type:    "thread_messages",
			Address: appstore.ConversationAddress{Kind: "channel", Ref: "300"},
		},
		{
			AgentID: exact,
			Type:    "thread_messages",
			Address: appstore.ConversationAddress{Kind: "thread", Ref: "300:500"},
		},
	}
	applyTestAppLaunchPolicy(t, apps.receipt, apps.appSetup, requests)
	plan, err := router.buildAppPlan(t.Context(), apps.receipt, apps.appSetup, requests)
	require.NoError(t, err)
	require.Len(t, plan, 1)
	for _, slot := range plan {
		require.Equal(t, exact, slot.AgentID)
	}
	requests[0].candidates.Subscriptions = requests[0].candidates.Subscriptions[:1]
	applyTestAppLaunchPolicy(t, apps.receipt, apps.appSetup, requests)
	plan, err = router.buildAppPlan(t.Context(), apps.receipt, apps.appSetup, requests)
	require.NoError(t, err)
	require.Empty(t, plan, "parent authority must not subscribe every thread")
}

func TestAppPlanLaunchesOnlyExplicitIntents(t *testing.T) {
	router, execution, apps, app, event := appPlannerFixture(t)
	requests, err := prepareAppEvents([]AppEvent{event}, apps.appSetup)
	require.NoError(t, err)
	requests[0].candidates.Launcher = &app
	plan, err := router.buildAppPlan(t.Context(), apps.receipt, apps.appSetup, requests)
	require.NoError(t, err)
	require.Empty(t, plan, "matching saved setups do not authorize an implicit launch")
	require.Zero(t, execution.reads)

	requests[0].event.Event.Mentioned = false
	requests[0].event.Directed = true
	requests[0].event.Launches = []AppLaunchIntent{{AppID: app.ID, Slot: "b", ProfileID: execution.profile.ID}}
	requests[0].candidates.Subscriptions = []appstore.AppSubscriptionRecord{
		{AgentID: uuid.New(), Type: "thread_messages", Address: requests[0].address},
	}
	plan, err = router.buildAppPlan(t.Context(), apps.receipt, apps.appSetup, requests)
	require.NoError(t, err)
	require.Len(t, plan, 1)
	for _, slot := range plan {
		require.NotNil(t, slot.Launch)
		require.Equal(t, "b", slot.Selection.Slot)
	}
}

func TestAppPlanRejectsUnavailableLaunchIntents(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*appEventCandidates)
	}{
		{"absent app", func(r *appEventCandidates) { r.candidates.Launcher = nil }},
		{"disabled app", func(r *appEventCandidates) {
			r.candidates.Launcher.State = appstore.ProjectAppStateDisconnected
		}},
		{"wrong project", func(r *appEventCandidates) { r.candidates.Launcher.ProjectID = uuid.New() }},
		{"removed launcher", func(r *appEventCandidates) { r.candidates.Launcher.Settings.Launcher = nil }},
		{"removed slot", func(r *appEventCandidates) { r.candidates.Launcher.Settings.Launcher.Slots = nil }},
		{"retargeted profile", func(r *appEventCandidates) {
			other := uuid.New()
			r.candidates.Launcher.Settings.Launcher.Slots[0].AgentProfileID = &other
		}},
		{"changed to fixed agent", func(r *appEventCandidates) {
			other := uuid.New()
			r.candidates.Launcher.Settings.Launcher.Slots[0] = appstore.AppLaunchSlot{
				Key: "a", AgentID: &other,
			}
		}},
		{"missing expected recipient", func(r *appEventCandidates) { r.event.Launches[0].ProfileID = uuid.Nil }},
		{"ambiguous recipient", func(r *appEventCandidates) { r.event.Launches[0].AgentID = uuid.New() }},
		{"retargeted fixed agent", func(r *appEventCandidates) {
			actual, expected := uuid.New(), uuid.New()
			r.candidates.Launcher.Settings.Launcher.Slots[0] = appstore.AppLaunchSlot{
				Key: "a", AgentID: &actual,
			}
			r.event.Launches[0].ProfileID, r.event.Launches[0].AgentID = uuid.Nil, expected
		}},
		{"retired selection", func(r *appEventCandidates) {
			now := time.Now()
			r.candidates.Selections = []appstore.AppTargetRecord{{
				AppID: r.event.Launches[0].AppID, SelectionSlot: "a", AgentID: uuid.New(), DeletedAt: &now,
			}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			router, execution, apps, app, event := appPlannerFixture(t)
			event.Launches = []AppLaunchIntent{{AppID: app.ID, Slot: "a", ProfileID: execution.profile.ID}}
			requests, err := prepareAppEvents([]AppEvent{event}, apps.appSetup)
			require.NoError(t, err)
			requests[0].candidates.Launcher = &app
			test.change(&requests[0])
			plan, err := router.buildAppPlan(t.Context(), apps.receipt, apps.appSetup, requests)
			require.ErrorIs(t, err, ErrAppLaunchUnavailable)
			require.Empty(t, plan)
			require.Zero(t, execution.reads, "stale choices must fail before profile derivation")
		})
	}
}

func TestAppPlanDirectedExpansionReusesSelectedIdentity(t *testing.T) {
	for _, settled := range []bool{false, true} {
		name := "planned in expansion"
		if settled {
			name = "settled without subscription"
		}
		t.Run(name, func(t *testing.T) {
			router, execution, apps, app, first := appPlannerFixture(t)
			first.Directed = true
			first.Launches = []AppLaunchIntent{
				{AppID: app.ID, Slot: "a", ProfileID: execution.profile.ID},
				{AppID: app.ID, Slot: "b", ProfileID: execution.profile.ID},
			}
			second := first
			second.SemanticKey = "message:files"
			second.Event.Mentioned = false
			second.Launches = second.Launches[:1]
			oldArtifact := uuid.Must(uuid.NewV7())
			second.ContentBlocks = json.RawMessage(
				`[{"type":"media_ref","artifact_id":"` + oldArtifact.String() + `"}]`,
			)
			second.Files = []AppPlannedFile{{ArtifactID: oldArtifact, ProviderFileID: "F123"}}
			first.Launches = append(first.Launches, first.Launches[0])
			requests, err := prepareAppEvents([]AppEvent{first, second}, apps.appSetup)
			require.NoError(t, err)
			agents := map[string]uuid.UUID{"a": uuid.New(), "b": uuid.New()}
			for i := range requests {
				requests[i].candidates.Launcher = &app
				requests[i].candidates.Subscriptions = []appstore.AppSubscriptionRecord{
					{AgentID: uuid.New(), Type: "thread_messages", Address: requests[i].address},
				}
				if settled {
					for slot, agent := range agents {
						requests[i].candidates.Selections = append(requests[i].candidates.Selections,
							appstore.AppTargetRecord{
								AppID:         app.ID,
								SelectionSlot: slot,
								AgentID:       agent,
							})
					}
				}
			}
			plan, err := router.buildAppPlan(t.Context(), apps.receipt, apps.appSetup, requests)
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

func TestAppLaunchPreservesEntireExistingCapabilities(t *testing.T) {
	_, execution, _, app, event := appPlannerFixture(t)
	base := execution.profile.CurrentConfig
	var compiled agentconfig.Compiled
	require.NoError(t, json.Unmarshal(base.CompiledDefinition, &compiled))
	appID := compiled.Tools[toolcatalog.AppToolName(app.Name, "post_message")].AppID
	toolKey := toolcatalog.AppToolName(app.Name, "post_message")
	tool := compiled.Tools[toolKey]
	tool.Deferred = true
	compiled.Tools[toolKey] = tool
	compiled.InteractionHandlers = map[string]agentconfig.AppCapabilityCompiled{app.Name: {AppID: appID}}
	encoded, err := agentconfig.EncodeCompiled(compiled)
	require.NoError(t, err)
	base.CompiledDefinition, base.EffectiveDefinitionHash = encoded.CanonicalJSON, encoded.Hash
	derived, subscriptions, err := deriveAppLaunch(base, app, event.Event.Scope)
	require.NoError(t, err)
	require.Len(t, subscriptions, 1)
	require.Equal(t, "thread_messages", subscriptions[0].Type)
	other, otherSubscriptions, err := deriveAppLaunch(base, app, appdefinition.Scope{
		Slack: &appdefinition.SlackScope{ChannelID: "C456", ThreadTS: "7.8"},
	})
	require.NoError(t, err)
	require.Len(t, otherSubscriptions, 1)
	require.Equal(t, derived.EffectiveDefinitionHash, other.EffectiveDefinitionHash,
		"different conversations reuse the same app capability config")
	require.NotEqual(t, subscriptions[0].Conversation, otherSubscriptions[0].Conversation)

	var actual agentconfig.Compiled
	require.NoError(t, json.Unmarshal(derived.CompiledDefinition, &actual))
	require.Equal(t, tool, actual.Tools[toolKey])
	require.Equal(t, compiled.InteractionHandlers, actual.InteractionHandlers)
	require.Equal(t, appID, actual.Tools[toolcatalog.AppToolName(app.Name, "read")].AppID)
}

func TestAppLaunchSuppliesProviderCapabilitiesAndReplyContext(t *testing.T) {
	for _, test := range []struct {
		appType                    appdefinition.Type
		provider, subscriptionType string
		scope                      appdefinition.Scope
		address                    string
	}{
		{
			appdefinition.SlackThread, "slack", "thread_messages",
			appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: "C123", ThreadTS: "1.2"}},
			`{"channel_id":"C123","thread_ts":"1.2"}`,
		},
		{
			appdefinition.DiscordThread, "discord", "thread_messages",
			appdefinition.Scope{Discord: &appdefinition.DiscordScope{GuildID: "100", ChannelID: "300", ThreadID: "500"}},
			`{"guild_id":"100","channel_id":"300","thread_id":"500"}`,
		},
		{
			appdefinition.GitHubPR, "github", "pull_request",
			appdefinition.Scope{GitHub: &appdefinition.GitHubScope{RepositoryID: 9007199254740993, PullRequest: 42}},
			`{"repository_id":9007199254740993,"pull_request":42}`,
		},
	} {
		t.Run(test.provider, func(t *testing.T) {
			_, execution, _, app, _ := appPlannerFixture(t)
			base := execution.profile.CurrentConfig
			app.Provider, app.AppType, app.Name = test.provider, test.appType, "receiver"
			derived, subscriptions, err := deriveAppLaunch(base, app, test.scope)
			require.NoError(t, err)
			require.Len(t, subscriptions, 1)
			subscription := subscriptions[0]
			require.Equal(t, test.subscriptionType, subscription.Type)
			var compiled agentconfig.Compiled
			require.NoError(t, json.Unmarshal(derived.CompiledDefinition, &compiled))
			definition, _ := appdefinition.Lookup(app.AppType)
			for _, operation := range definition.Tools {
				require.Equal(t, app.ID, compiled.Tools[toolcatalog.AppToolName(app.Name, operation)].AppID)
			}
			require.Equal(t, app.ID, subscription.AppID)
			require.JSONEq(t, test.address, string(subscription.Conversation))
			require.ElementsMatch(t, definition.Subscriptions[subscription.Type].Events, subscription.Events)
			if definition.InteractionHandler != nil {
				require.NotEmpty(t, compiled.InteractionHandlers[app.Name].AppID)
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
			content, err := appdefinition.AppendInputContext(app.Name, test.scope, json.RawMessage(`[{"type":"text","text":"hello"}]`))
			require.NoError(t, err)
			var blocks []struct {
				Text     string
				Metadata map[string]string
			}
			require.NoError(t, json.Unmarshal(content, &blocks))
			require.Equal(t, "hello", blocks[0].Text)
			require.Equal(t, "true", blocks[1].Metadata["omnara_hidden"])
			require.Contains(t, blocks[1].Text, `"app":"receiver"`)
			require.Contains(t, blocks[1].Text, test.address)
		})
	}
}

func TestAppLaunchSubscriptionsFollowDefinition(t *testing.T) {
	appID := uuid.New()
	scope := appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: "C123", ThreadTS: "1.2"}}
	definition := appdefinition.Definition{
		Provider: appdefinition.ProviderSlack,
		Subscriptions: map[string]appdefinition.SubscriptionDefinition{
			"updates": {Name: "updates", Provider: appdefinition.ProviderSlack, Events: []string{"message"}},
		},
		InitialSubscription: "updates",
	}
	subscriptions, err := appLaunchSubscriptions(appID, definition, scope)
	require.NoError(t, err)
	require.Equal(t, []appstore.AppSubscriptionAttachment{{
		AppID: appID, Type: "updates", Events: []string{"message"},
		Conversation: json.RawMessage(`{"channel_id":"C123","thread_ts":"1.2"}`),
	}}, subscriptions)

	definition.InitialSubscription = ""
	subscriptions, err = appLaunchSubscriptions(appID, definition, scope)
	require.NoError(t, err)
	require.Empty(t, subscriptions, "exported subscriptions do not imply a launch attachment")
	definition.Subscriptions = nil
	subscriptions, err = appLaunchSubscriptions(appID, definition, scope)
	require.NoError(t, err)
	require.Empty(t, subscriptions, "launches need not export any subscription")
	_, err = appLaunchSubscriptions(appID, definition, appdefinition.Scope{})
	require.Error(t, err, "a launch still requires a valid event scope without subscriptions")

	definition.InitialSubscription = "missing"
	_, err = appLaunchSubscriptions(appID, definition, scope)
	require.EqualError(t, err, `app initial subscription "missing" is not defined`)
}

func TestAppLaunchPreservesInteractionToolOverrides(t *testing.T) {
	_, execution, _, app, event := appPlannerFixture(t)
	base := execution.profile.CurrentConfig
	var compiled agentconfig.Compiled
	require.NoError(t, json.Unmarshal(base.CompiledDefinition, &compiled))
	compiled.InteractionHandlers = map[string]agentconfig.AppCapabilityCompiled{app.Name: {AppID: app.ID}}
	compiled.Tools[toolcatalog.ToolNameListInteractionHandlers] = agentconfig.ToolCompiled{
		Enabled: true, Deferred: true, Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk),
	}
	compiled.Tools[toolcatalog.ToolNameSetInteractionHandler] = agentconfig.ToolCompiled{
		Enabled: false, Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysDeny),
	}
	encoded, err := agentconfig.EncodeCompiled(compiled)
	require.NoError(t, err)
	base.CompiledDefinition, base.EffectiveDefinitionHash = encoded.CanonicalJSON, encoded.Hash
	for _, appType := range []appdefinition.Type{
		appdefinition.SlackThread, appdefinition.DiscordThread, appdefinition.GitHubPR,
	} {
		t.Run(string(appType), func(t *testing.T) {
			app := app
			var scope appdefinition.Scope
			app.AppType = appType
			switch appType {
			case appdefinition.SlackThread:
				scope = event.Event.Scope
			case appdefinition.DiscordThread:
				app.ID, app.Name, app.Provider = uuid.New(), "discord", appdefinition.ProviderDiscord
				scope = appdefinition.Scope{Discord: &appdefinition.DiscordScope{GuildID: "100", ChannelID: "300", ThreadID: "500"}}
			case appdefinition.GitHubPR:
				app.ID, app.Name, app.Provider = uuid.New(), "github", appdefinition.ProviderGitHub
				scope = appdefinition.Scope{GitHub: &appdefinition.GitHubScope{RepositoryID: 123, PullRequest: 42}}
			}
			derived, _, err := deriveAppLaunch(base, app, scope)
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

func TestAppPlanFrozenSubscriptionsRouteLaterMessagesAndMedia(t *testing.T) {
	for _, provider := range []string{
		appdefinition.ProviderSlack, appdefinition.ProviderDiscord, appdefinition.ProviderGitHub,
	} {
		t.Run(provider, func(t *testing.T) {
			router, execution, apps, app, first := appPlannerFixture(t)
			app.Name = "receiver"
			app.Settings.Launcher.Slots = app.Settings.Launcher.Slots[:1]
			subscriptionType, kind := "thread_messages", "message"
			expectedEvents := []string{"message"}
			otherScope := appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: "C123", ThreadTS: "1.3"}}
			switch provider {
			case appdefinition.ProviderDiscord:
				app.Provider, app.AppType, app.ProviderTenantID = provider, appdefinition.DiscordThread, "11"
				first.Event.Scope = appdefinition.Scope{
					Discord: &appdefinition.DiscordScope{GuildID: "100", ChannelID: "300", ThreadID: "500"},
				}
				otherScope = appdefinition.Scope{
					Discord: &appdefinition.DiscordScope{GuildID: "100", ChannelID: "300", ThreadID: "501"},
				}
			case appdefinition.ProviderGitHub:
				app.Provider, app.AppType = provider, appdefinition.GitHubPR
				app.ProviderTenantID, app.ProviderAccountRef = "11", "22"
				first.Event.Scope = appdefinition.Scope{GitHub: &appdefinition.GitHubScope{RepositoryID: 123, PullRequest: 42}}
				otherScope = appdefinition.Scope{GitHub: &appdefinition.GitHubScope{RepositoryID: 123, PullRequest: 43}}
				first.Event.Kind = "pull_request_opened"
				subscriptionType, kind = "pull_request", "commit"
				expectedEvents = []string{"commit", "discussion_comment", "review_comment"}
			}
			apps.appSetup = app
			first.Actor = appTestActor(t, app.ID, first.Actor.ProviderUserID)
			first.SemanticKey = "z:launch"
			first.Launches = []AppLaunchIntent{{AppID: app.ID, Slot: "a", ProfileID: execution.profile.ID}}
			reply := first
			reply.Launches, reply.Event.Mentioned, reply.Event.Kind = nil, false, kind
			reply.SemanticKey = "b:reply"
			before := reply
			before.SemanticKey = "c:before-launch"
			media := reply
			media.SemanticKey = "a:media"
			placeholder := uuid.New()
			media.ContentBlocks = json.RawMessage(`[{"type":"media_ref","artifact_id":"` + placeholder.String() + `"}]`)
			media.Files = []AppPlannedFile{{ArtifactID: placeholder, ProviderFileID: "F123"}}
			other := reply
			other.SemanticKey, other.Event.Scope = "other:conversation", otherScope
			events := []AppEvent{before, first, reply, media, other}
			if provider == appdefinition.ProviderGitHub {
				excluded := reply
				excluded.SemanticKey, excluded.Event.Kind = "excluded:event", "pull_request_opened"
				events = append(events, excluded)
			}
			requests, err := prepareAppEvents(events, app)
			require.NoError(t, err)
			for i := range requests {
				requests[i].candidates.Launcher = &app
			}
			plan, err := router.buildAppPlan(t.Context(), apps.receipt, app, requests)
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
				require.Equal(t, app.ID, subscription.AppID)
				require.Equal(t, subscriptionType, subscription.Type)
				require.Equal(t, expectedEvents, subscription.Events, "defaults are resolved and canonical before freezing")
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
					Event: kind, Alternatives: []executionstore.InboxSubscriptionReference{{
						Type: subscriptionType, Address: appstore.ConversationAddress{Kind: addressKind, Ref: addressRef},
					}},
				}, slot.Subscription)
				if slot.EventOrder == 3 {
					require.Len(t, slot.Files, 1)
					require.NotEqual(t, placeholder, slot.Files[0].ArtifactID)
					require.Equal(t, "F123", slot.Files[0].ProviderFileID)
				} else {
					require.Equal(t, 2, slot.EventOrder)
				}
			}
			apps.receipt.Plan, err = json.Marshal(plan)
			require.NoError(t, err)
			execution.profile.CurrentConfig = executionstore.AgentConfigRecord{}
			apps.appSetup.AppType = "no-longer-available"
			replayed, err := router.Freeze(t.Context(), apps.receipt.Lease(), []AppEvent{other})
			require.NoError(t, err)
			replayedJSON, err := json.Marshal(replayed)
			require.NoError(t, err)
			require.JSONEq(t, string(apps.receipt.Plan), string(replayedJSON))
		})
	}
}
