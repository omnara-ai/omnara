package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

type appPlanExecution struct {
	AppExecutionStore
	profile executionstore.AgentProfileRecord
	reads   int
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

type appPlanIntegrations struct {
	AppRoutingStore
	appSetup integrationstore.ProjectAppRecord
	receipt  integrationstore.IntegrationInboxRecord
}

func (s *appPlanIntegrations) GetProjectAppByID(
	context.Context,
	uuid.UUID,
) (integrationstore.ProjectAppRecord, error) {
	return s.appSetup, nil
}

func (s *appPlanIntegrations) GetIntegrationInbox(
	context.Context,
	uuid.UUID,
	uuid.UUID,
) (integrationstore.IntegrationInboxRecord, error) {
	return s.receipt, nil
}

func appPlannerFixture(
	t *testing.T,
) (*AppRouter, *appPlanExecution, *appPlanIntegrations, integrationstore.ProjectAppRecord, AppEvent) {
	t.Helper()
	project, org, appID := uuid.New(), uuid.New(), uuid.New()
	publicApp, err := publicid.Encode(publicid.KindProjectApp, appID)
	require.NoError(t, err)
	disabled := false
	source := agentconfig.AgentConfigSource{
		Instruction: "Pinned original",
		Model:       agentconfig.AgentConfigModelSource{ProviderConfig: "test", Name: "model"},
		Tools: map[string]agentconfig.AgentConfigToolSource{
			toolcatalog.AppToolName("chat", "post_message"): {Enabled: &disabled},
		},
	}
	raw, err := json.Marshal(source)
	require.NoError(t, err)
	compiled, err := agentconfig.Compile(agentconfig.SourceFormatJSON, raw, agentconfig.CompileOptions{
		ResolveAppName: func(string) (agentconfig.AppResolution, error) {
			return agentconfig.AppResolution{AppID: publicApp, Definition: appdefinition.Slack}, nil
		},
	})
	require.NoError(t, err)
	base := executionstore.AgentConfigRecord{
		ID:                      uuid.New(),
		OrgID:                   org,
		ProjectID:               project,
		Source:                  "deliberately unusable source",
		ConfiguredModelID:       uuid.New(),
		CompiledDefinition:      compiled.CanonicalJSON,
		CompilerVersion:         agentconfig.CompilerVersion,
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
	integrations := &appPlanIntegrations{
		appSetup: integrationstore.ProjectAppRecord{
			ID:   appID,
			Name: "chat", DefinitionID: appdefinition.Slack,
			OrgID:             org,
			ProjectID:         project,
			Provider:          "slack",
			ProviderTenantID:  "T123",
			State:             integrationstore.ProjectAppStateActive,
			InstalledByUserID: uuid.New(),
		},
	}
	integrations.receipt = integrationstore.IntegrationInboxRecord{
		IntegrationInboxSummary: integrationstore.IntegrationInboxSummary{
			ID:        uuid.New(),
			ProjectID: project,
			AppID:     integrations.appSetup.ID,
		},
	}
	app := integrations.appSetup
	app.Settings = integrationstore.ProjectAppSettings{
		Launcher: &integrationstore.AppLauncher{
			Trigger:   "mention",
			ScopeKind: "workspace",
			ScopeRef:  "T123",
			Slots: []integrationstore.AppLaunchSlot{
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
		Actor:         executionstore.ActorParams{Provider: "slack", ProviderTenantID: "T123", ProviderUserID: "U123"},
	}
	return NewAppRouter(execution, integrations), execution, integrations, app, event
}

func TestAppPlanPinsFullProfileMembershipAndCompiledPolicy(t *testing.T) {
	router, execution, integrations, app, event := appPlannerFixture(t)
	oldID := uuid.Must(uuid.NewV7())
	event.ContentBlocks = json.RawMessage(
		`[{"type":"text","text":"review"},{"type":"media_ref","artifact_id":"` + oldID.String() + `"}]`,
	)
	event.Files = []AppPlannedFile{{ArtifactID: oldID, ProviderFileID: "F123"}}
	requests, err := prepareAppEvents([]AppEvent{event}, integrations.appSetup)
	require.NoError(t, err)
	requests[0].candidates.Launcher = &app
	applyTestAppLaunchPolicy(t, integrations.receipt, integrations.appSetup, requests)
	plan, err := router.buildAppPlan(t.Context(), integrations.receipt, integrations.appSetup, requests)
	require.NoError(t, err)
	require.Len(t, plan, 2)
	require.Equal(t, 1, execution.reads)
	agents, artifacts := map[uuid.UUID]bool{}, map[uuid.UUID]bool{}
	for _, slot := range plan {
		require.Equal(t, uuid.Version(7), slot.AgentID.Version())
		require.False(t, agents[slot.AgentID])
		agents[slot.AgentID] = true
		require.Equal(t, execution.profile.CurrentConfig.ID, slot.BaseConfigID)
		require.Equal(t, execution.profile.CurrentConfig.EffectiveDefinitionHash, slot.BaseConfigHash)
		require.Empty(t, slot.Launch.DerivedConfig.Source)
		require.Nil(t, slot.Input)
		require.Len(t, slot.Files, 1)
		require.False(t, artifacts[slot.Files[0].ArtifactID])
		artifacts[slot.Files[0].ArtifactID] = true
		require.NotEqual(t, oldID, slot.Files[0].ArtifactID)
		var compiled agentconfig.Compiled
		require.NoError(t, json.Unmarshal(slot.Launch.DerivedConfig.CompiledDefinition, &compiled))
		require.Equal(t, "Pinned original", compiled.Instruction)
		require.False(t, compiled.Tools[toolcatalog.AppToolName("chat", "post_message")].Enabled)
		key := app.Name + "__thread_messages"
		require.Equal(t, key, slot.ListenerKey)
		require.Equal(t, compiled.Tools[toolcatalog.AppToolName(app.Name, "read")].AppID, compiled.Listeners[key].AppID)
		require.JSONEq(t, `{"channel_id":"C123","thread_ts":"1.2"}`,
			string(compiled.Tools[toolcatalog.AppToolName(app.Name, "read")].Config))
		require.JSONEq(t, `{"events":["message"]}`, string(compiled.Listeners[key].Config))
		require.NotEmpty(t, compiled.InteractionHandlers[app.Name].AppID)
		raw, err := json.Marshal(slot)
		require.NoError(t, err)
		var kernel executionstore.InboxLaunchSlot
		require.NoError(t, json.Unmarshal(raw, &kernel))
		require.Equal(t, *slot.Selection, kernel.Selection)
		require.Equal(t, slot.AgentID, kernel.AgentID)
	}
	// Frozen replay is read-only even after provider/app/profile changes. The
	// embedded nil store interface would panic if replanning were attempted.
	integrations.receipt.Plan, err = json.Marshal(plan)
	require.NoError(t, err)
	integrations.appSetup.State = integrationstore.ProjectAppStateDisconnected
	got, err := router.Freeze(t.Context(), integrations.receipt.Lease(), nil)
	require.NoError(t, err)
	require.Equal(t, plan, got)
	got, err = freezeTestAppEvents(t.Context(), router, integrations.receipt.Lease(), nil)
	require.NoError(t, err, "the policy helper must also leave frozen retries untouched")
	require.Equal(t, plan, got)
}

func TestAppPlanExistingTriggersOverlapAndRetiredSelection(t *testing.T) {
	router, execution, integrations, app, event := appPlannerFixture(t)
	agent := uuid.New()
	app.Settings.Launcher.Slots = append(
		app.Settings.Launcher.Slots,
		integrationstore.AppLaunchSlot{Key: "trigger", AgentID: &agent},
	)
	requests, err := prepareAppEvents([]AppEvent{event}, integrations.appSetup)
	require.NoError(t, err)
	requests[0].candidates = integrationstore.AppRoutingCandidates{
		Launcher: &app,
		Listeners: []integrationstore.AgentListenerRecord{
			{
				AgentID:     agent,
				ListenerKey: "channel",
				Address:     integrationstore.ConversationAddress{Kind: "channel", Ref: "C123"},
			},
			{
				AgentID:     agent,
				ListenerKey: "another",
				Address:     integrationstore.ConversationAddress{Kind: "channel", Ref: "C123"},
			},
		},
		Selections: []integrationstore.IntegrationTargetRecord{{AppID: app.ID, SelectionSlot: "removed-old-slot"}},
	}
	applyTestAppLaunchPolicy(t, integrations.receipt, integrations.appSetup, requests)
	plan, err := router.buildAppPlan(t.Context(), integrations.receipt, integrations.appSetup, requests)
	require.NoError(t, err)
	require.Len(t, plan, 1)
	require.Zero(t, execution.reads)
	for _, slot := range plan {
		require.Nil(t, slot.Selection)
		require.Nil(t, slot.Launch)
		require.Nil(t, slot.Listener, "explicit trigger is independent of listener revocation")
		require.Equal(t, agent, slot.AgentID)
	}
	// A matching exact continuation suppresses new profiles; a broad channel
	// listener alone does not suppress an unrelated thread's first selection.
	requests[0].candidates.Selections = nil
	requests[0].candidates.Listeners[0].Address = requests[0].address
	applyTestAppLaunchPolicy(t, integrations.receipt, integrations.appSetup, requests)
	plan, err = router.buildAppPlan(t.Context(), integrations.receipt, integrations.appSetup, requests)
	require.NoError(t, err)
	require.Len(t, plan, 1)
	requests[0].candidates.Listeners[0].Address = integrationstore.ConversationAddress{Kind: "channel", Ref: "C123"}
	applyTestAppLaunchPolicy(t, integrations.receipt, integrations.appSetup, requests)
	plan, err = router.buildAppPlan(t.Context(), integrations.receipt, integrations.appSetup, requests)
	require.NoError(t, err)
	require.Len(t, plan, 3)
}

func TestAppPlanExpansionContinuesLauncherRuntimeListener(t *testing.T) {
	router, _, integrations, app, first := appPlannerFixture(t)
	second := first
	second.SemanticKey = "message:0"
	second.Event.Mentioned = false
	requests, err := prepareAppEvents([]AppEvent{first, second}, integrations.appSetup)
	require.NoError(t, err)
	for i := range requests {
		requests[i].candidates.Launcher = &app
	}
	applyTestAppLaunchPolicy(t, integrations.receipt, integrations.appSetup, requests)
	plan, err := router.buildAppPlan(t.Context(), integrations.receipt, integrations.appSetup, requests)
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
			require.NotNil(t, slot.Listener)
			require.Equal(t, second.SemanticKey, slot.Input.IdempotencyKey)
		}
	}
	require.Equal(t, 2, launches)
	require.Equal(t, 2, inputs)
}

func TestAppPlanDiscordThreadRequiresExactListener(t *testing.T) {
	router, _, integrations, _, event := appPlannerFixture(t)
	integrations.appSetup.Provider = "discord"
	integrations.appSetup.ProviderTenantID = "11"
	event.Actor.Provider, event.Actor.ProviderTenantID = "discord", "11"
	event.Event.Scope = appdefinition.Scope{
		Discord: &appdefinition.DiscordScope{GuildID: "100", ChannelID: "300", ThreadID: "500"},
	}
	event.Event.Mentioned = false
	requests, err := prepareAppEvents([]AppEvent{event}, integrations.appSetup)
	require.NoError(t, err)
	parent, exact := uuid.New(), uuid.New()
	requests[0].candidates.Listeners = []integrationstore.AgentListenerRecord{
		{
			AgentID:     parent,
			ListenerKey: "channel",
			Address:     integrationstore.ConversationAddress{Kind: "channel", Ref: "300"},
		},
		{
			AgentID:     parent,
			ListenerKey: "guild",
			Address:     integrationstore.ConversationAddress{Kind: "guild", Ref: "100"},
		},
		{
			AgentID:     exact,
			ListenerKey: "thread",
			Address:     integrationstore.ConversationAddress{Kind: "thread", Ref: "300:500"},
		},
	}
	applyTestAppLaunchPolicy(t, integrations.receipt, integrations.appSetup, requests)
	plan, err := router.buildAppPlan(t.Context(), integrations.receipt, integrations.appSetup, requests)
	require.NoError(t, err)
	require.Len(t, plan, 1)
	for _, slot := range plan {
		require.Equal(t, exact, slot.AgentID)
	}
	requests[0].candidates.Listeners = requests[0].candidates.Listeners[:2]
	applyTestAppLaunchPolicy(t, integrations.receipt, integrations.appSetup, requests)
	plan, err = router.buildAppPlan(t.Context(), integrations.receipt, integrations.appSetup, requests)
	require.NoError(t, err)
	require.Empty(t, plan, "parent authority must not subscribe every thread")
}

func TestAppPlanLaunchesOnlyExplicitIntents(t *testing.T) {
	router, execution, integrations, app, event := appPlannerFixture(t)
	requests, err := prepareAppEvents([]AppEvent{event}, integrations.appSetup)
	require.NoError(t, err)
	requests[0].candidates.Launcher = &app
	plan, err := router.buildAppPlan(t.Context(), integrations.receipt, integrations.appSetup, requests)
	require.NoError(t, err)
	require.Empty(t, plan, "matching saved setups do not authorize an implicit launch")
	require.Zero(t, execution.reads)

	// An app may explicitly choose one profile even when ordinary trigger and
	// exact-continuation policy would choose none. Directed input stays exclusive.
	requests[0].event.Event.Mentioned = false
	requests[0].event.Directed = true
	requests[0].event.Launches = []AppLaunchIntent{{AppID: app.ID, Slot: "b", ProfileID: execution.profile.ID}}
	requests[0].candidates.Listeners = []integrationstore.AgentListenerRecord{
		{AgentID: uuid.New(), ListenerKey: "unrelated", Address: requests[0].address},
	}
	plan, err = router.buildAppPlan(t.Context(), integrations.receipt, integrations.appSetup, requests)
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
			r.candidates.Launcher.State = integrationstore.ProjectAppStateDisconnected
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
			r.candidates.Launcher.Settings.Launcher.Slots[0] = integrationstore.AppLaunchSlot{
				Key: "a", AgentID: &other,
			}
		}},
		{"missing expected recipient", func(r *appEventCandidates) { r.event.Launches[0].ProfileID = uuid.Nil }},
		{"ambiguous recipient", func(r *appEventCandidates) { r.event.Launches[0].AgentID = uuid.New() }},
		{"retargeted fixed agent", func(r *appEventCandidates) {
			actual, expected := uuid.New(), uuid.New()
			r.candidates.Launcher.Settings.Launcher.Slots[0] = integrationstore.AppLaunchSlot{
				Key: "a", AgentID: &actual,
			}
			r.event.Launches[0].ProfileID, r.event.Launches[0].AgentID = uuid.Nil, expected
		}},
		{"retired selection", func(r *appEventCandidates) {
			now := time.Now()
			r.candidates.Selections = []integrationstore.IntegrationTargetRecord{{
				AppID: r.event.Launches[0].AppID, SelectionSlot: "a", AgentID: uuid.New(), DeletedAt: &now,
			}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			router, execution, integrations, app, event := appPlannerFixture(t)
			event.Launches = []AppLaunchIntent{{AppID: app.ID, Slot: "a", ProfileID: execution.profile.ID}}
			requests, err := prepareAppEvents([]AppEvent{event}, integrations.appSetup)
			require.NoError(t, err)
			requests[0].candidates.Launcher = &app
			test.change(&requests[0])
			plan, err := router.buildAppPlan(t.Context(), integrations.receipt, integrations.appSetup, requests)
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
			name = "settled without listener"
		}
		t.Run(name, func(t *testing.T) {
			router, execution, integrations, app, first := appPlannerFixture(t)
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
			// Duplicate intent within an event must not create a second launch or
			// an extra initial input for that same slot.
			first.Launches = append(first.Launches, first.Launches[0])
			requests, err := prepareAppEvents([]AppEvent{first, second}, integrations.appSetup)
			require.NoError(t, err)
			agents := map[string]uuid.UUID{"a": uuid.New(), "b": uuid.New()}
			for i := range requests {
				requests[i].candidates.Launcher = &app
				requests[i].candidates.Listeners = []integrationstore.AgentListenerRecord{
					{AgentID: uuid.New(), ListenerKey: "unrelated", Address: requests[i].address},
				}
				if settled {
					for slot, agent := range agents {
						requests[i].candidates.Selections = append(requests[i].candidates.Selections,
							integrationstore.IntegrationTargetRecord{
								AppID:         app.ID,
								SelectionSlot: slot,
								AgentID:       agent,
								RoutingRole:   integrationstore.TargetSelected,
							})
					}
				}
			}
			plan, err := router.buildAppPlan(t.Context(), integrations.receipt, integrations.appSetup, requests)
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
				require.Nil(t, slot.Listener, "explicit original-source delivery does not depend on a listener")
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
			require.Equal(t, 1, inputs, "directed files must skip even listeners planned earlier in this expansion")
		})
	}
}

func TestAppLaunchPreservesEntireExistingCapabilities(t *testing.T) {
	_, execution, _, app, event := appPlannerFixture(t)
	base := execution.profile.CurrentConfig
	var compiled agentconfig.Compiled
	require.NoError(t, json.Unmarshal(base.CompiledDefinition, &compiled))
	appID := compiled.Tools[toolcatalog.AppToolName(app.Name, "post_message")].AppID
	toolKey, listenerKey := toolcatalog.AppToolName(app.Name, "post_message"), app.Name+"__thread_messages"
	tool := compiled.Tools[toolKey]
	tool.Config = json.RawMessage(`{"channel_id":"C999","thread_ts":"9.9"}`)
	tool.Deferred = true
	compiled.Tools[toolKey] = tool
	compiled.Listeners = map[string]agentconfig.AppCapabilityCompiled{listenerKey: {AppID: appID, Config: json.RawMessage(`{}`)}}
	compiled.InteractionHandlers = map[string]agentconfig.AppCapabilityCompiled{app.Name: {AppID: appID, Config: json.RawMessage(`{"channel_id":"C999"}`)}}
	encoded, err := agentconfig.EncodeCompiled(compiled)
	require.NoError(t, err)
	base.CompiledDefinition, base.EffectiveDefinitionHash = encoded.CanonicalJSON, encoded.Hash
	derived, key, err := deriveAppLaunch(base, app, event.Event.Scope)
	require.NoError(t, err)
	require.Equal(t, listenerKey, key, "launch admission follows this listener independently of its empty config")
	var actual agentconfig.Compiled
	require.NoError(t, json.Unmarshal(derived.Config.CompiledDefinition, &actual))
	require.Equal(t, tool, actual.Tools[toolKey])
	require.Equal(t, compiled.Listeners, actual.Listeners)
	require.Equal(t, compiled.InteractionHandlers, actual.InteractionHandlers)
	require.JSONEq(t, `{"channel_id":"C123","thread_ts":"1.2"}`,
		string(actual.Tools[toolcatalog.AppToolName(app.Name, "read")].Config))

	other, err := publicid.Encode(publicid.KindProjectApp, uuid.New())
	require.NoError(t, err)
	compiled.Listeners[listenerKey] = agentconfig.AppCapabilityCompiled{AppID: other, Config: json.RawMessage(`{}`)}
	encoded, err = agentconfig.EncodeCompiled(compiled)
	require.NoError(t, err)
	base.CompiledDefinition, base.EffectiveDefinitionHash = encoded.CanonicalJSON, encoded.Hash
	_, _, err = deriveAppLaunch(base, app, event.Event.Scope)
	require.ErrorIs(t, err, ErrAppLaunchUnavailable, "a reused app name must not retarget its pinned listener")
}

func TestAppLaunchSuppliesProviderCapabilitiesAndReplyContext(t *testing.T) {
	for _, test := range []struct {
		definition, provider, listener string
		scope                          appdefinition.Scope
		address                        string
	}{
		{
			appdefinition.Slack, "slack", "thread_messages",
			appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: "C123", ThreadTS: "1.2"}},
			`{"channel_id":"C123","thread_ts":"1.2"}`,
		},
		{
			appdefinition.Discord, "discord", "thread_messages",
			appdefinition.Scope{Discord: &appdefinition.DiscordScope{GuildID: "100", ChannelID: "300", ThreadID: "500"}},
			`{"guild_id":"100","channel_id":"300","thread_id":"500"}`,
		},
		{
			appdefinition.GitHub, "github", "pull_request",
			appdefinition.Scope{GitHub: &appdefinition.GitHubScope{RepositoryID: 9007199254740993, PullRequest: 42}},
			`{"repository_id":9007199254740993,"pull_request":42}`,
		},
	} {
		t.Run(test.provider, func(t *testing.T) {
			_, execution, _, app, _ := appPlannerFixture(t)
			base := execution.profile.CurrentConfig
			app.Provider, app.DefinitionID, app.Name = test.provider, test.definition, "receiver"
			derived, key, err := deriveAppLaunch(base, app, test.scope)
			require.NoError(t, err)
			require.Equal(t, "receiver__"+test.listener, key)
			var compiled agentconfig.Compiled
			require.NoError(t, json.Unmarshal(derived.Config.CompiledDefinition, &compiled))
			definition, _ := appdefinition.Lookup(app.DefinitionID)
			for _, operation := range definition.Tools {
				require.JSONEq(t, test.address, string(compiled.Tools[toolcatalog.AppToolName(app.Name, operation)].Config))
			}
			prepared, err := definition.Listeners[test.listener].Prepare(compiled.Listeners[key].Config)
			require.NoError(t, err)
			require.Empty(t, prepared.Conversations)
			if definition.InteractionHandler != nil {
				require.JSONEq(t, test.address, string(compiled.InteractionHandlers[app.Name].Config))
			} else {
				require.Empty(t, compiled.InteractionHandlers)
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
