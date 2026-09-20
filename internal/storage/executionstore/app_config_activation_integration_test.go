//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

type appActivationFixture struct {
	ctx     context.Context //nolint:containedctx // Shared test fixture retains only its test-lifetime context.
	store   *Store
	user    identitystore.UserRecord
	profile executionstore.AgentProfileRecord
	app     integrationstore.ProjectAppRecord
}

func newAppActivationFixture(t *testing.T) appActivationFixture {
	t.Helper()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	user := createIntegrationProjectAdmin(t, ctx, store, "app-activation@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, "app-activation")
	app := (appInteractionFixture{ctx: ctx, store: store, user: user}).createApp(t, "chat")

	return appActivationFixture{ctx: ctx, store: store, user: user, profile: profile, app: app}
}

func (f appActivationFixture) listener() agentconfig.AppCapabilityCompiled {
	return agentconfig.AppCapabilityCompiled{
		AppID:  publicResourceID(publicid.KindProjectApp, f.app.ID),
		Config: json.RawMessage(`{"conversations":[{"channel_id":"C123"}]}`),
	}
}

func (f appActivationFixture) definition(
	t *testing.T,
	instruction string,
	listeners map[string]agentconfig.AppCapabilityCompiled,
) executionstore.CreateAgentConfigInput {
	t.Helper()
	var compiled agentconfig.Compiled
	require.NoError(t, json.Unmarshal(f.profile.CurrentConfig.CompiledDefinition, &compiled))
	compiled.Instruction, compiled.Listeners = instruction, listeners
	return f.encodedDefinition(t, compiled)
}

func (f appActivationFixture) encodedDefinition(
	t *testing.T,
	compiled agentconfig.Compiled,
) executionstore.CreateAgentConfigInput {
	t.Helper()
	encoded, err := agentconfig.EncodeCompiled(compiled)
	require.NoError(t, err)
	return executionstore.CreateAgentConfigInput{
		ProjectID: testProjectID, ConfiguredModelID: f.profile.CurrentConfig.ConfiguredModelID,
		CompiledDefinition: encoded.CanonicalJSON, CompilerVersion: agentconfig.CompilerVersion,
		EffectiveDefinitionHash: encoded.Hash,
	}
}

func (f appActivationFixture) withSendingTools(
	t *testing.T,
	input executionstore.CreateAgentConfigInput,
) executionstore.CreateAgentConfigInput {
	t.Helper()
	var compiled agentconfig.Compiled
	require.NoError(t, json.Unmarshal(input.CompiledDefinition, &compiled))
	if compiled.Tools == nil {
		compiled.Tools = map[string]agentconfig.ToolCompiled{}
	}
	for _, operation := range []string{"post_message", "read"} {
		compiled.Tools[toolcatalog.AppToolName(f.app.Name, operation)] = agentconfig.ToolCompiled{
			Enabled: true, AppID: publicResourceID(publicid.KindProjectApp, f.app.ID),
			Config:     json.RawMessage(`{"channel_id":"C123"}`),
			Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow),
		}
	}
	return f.encodedDefinition(t, compiled)
}

func (f appActivationFixture) launchInput(configID uuid.UUID, key string) executionstore.LaunchAgentInput {
	return executionstore.LaunchAgentInput{
		ProjectID:      testProjectID,
		AgentConfigID:  configID,
		LaunchedBy:     userPrincipal(f.user.ID),
		IdempotencyKey: key,
	}
}

func (f appActivationFixture) changeInput(
	t *testing.T,
	agentID uuid.UUID,
	instruction string,
	resources map[string]agentconfig.AppCapabilityCompiled,
	key string,
) executionstore.ChangeAgentConfigInput {
	return executionstore.ChangeAgentConfigInput{
		CreateAgentConfigInput: f.definition(t, instruction, resources),
		AgentID:                agentID,
		ActorType:              identitystore.PrincipalTypeUser,
		ActorID:                f.user.ID,
		Reason:                 "app-capabilities-test",
		IdempotencyKey:         key,
	}
}

func (f appActivationFixture) listeners(t *testing.T, agentID uuid.UUID) []dbsqlc.AgentListener {
	t.Helper()
	rows, err := f.store.q.ListActiveAgentListeners(
		f.ctx,
		dbsqlc.ListActiveAgentListenersParams{ProjectID: testProjectID, AgentID: agentID},
	)
	require.NoError(t, err)
	return rows
}

func (f appActivationFixture) disable(t *testing.T) {
	t.Helper()
	changed, err := f.store.Integrations().
		DisconnectProjectApp(f.ctx, integrationstore.DisconnectProjectAppInput{ProjectID: testProjectID, AppID: f.app.ID})
	require.NoError(t, err)
	require.True(t, changed)
}

func (f appActivationFixture) confirmedFollows(t *testing.T, agent executionstore.AgentRecord) []dbsqlc.AgentListener {
	t.Helper()
	lock, err := f.store.Execution().
		AcquireAgentRuntimeLock(f.ctx, testProjectID, agent.ID, testWorkerProcessID, testAgentRuntimeLockLeaseDuration)
	require.NoError(t, err)
	fixture := processDaemonFixture{Store: f.store, AgentID: agent.ID, UserID: f.user.ID, Lock: lock}
	ids := createToolCallBatchForProcessTest(t, f.ctx, fixture, "app-follow", []processToolCallBatchItem{
		{
			TestName: "follow-one",
			ToolName: "app__chat__post_message",
			ToolType: toolcatalog.ToolTypeBuiltIn,
			Allowed:  true,
			Input:    json.RawMessage(`{"text":"First thread","follow_replies":true}`),
		},
		{
			TestName: "follow-two",
			ToolName: "app__chat__post_message",
			ToolType: toolcatalog.ToolTypeBuiltIn,
			Allowed:  true,
			Input:    json.RawMessage(`{"text":"Second thread","follow_replies":true}`),
		},
	})
	var listeners []dbsqlc.AgentListener
	for index, ref := range []string{"C123:111.222", "C123:333.444"} {
		_, err := f.store.Execution().CompleteToolCall(f.ctx, executionstore.CompleteToolCallInput{
			ProjectID:          testProjectID,
			AgentID:            agent.ID,
			ID:                 ids[index],
			RuntimeLockID:      lock.ID,
			Outcome:            executionstore.ToolResultOutcomeSucceeded,
			ResultContentParts: json.RawMessage(`[{"type":"text","text":"posted"}]`),
		})
		require.NoError(t, err)
		// Seed the confirmed provider address against a real completed tool call.
		// Provider follow admission is separate from config activation.
		listener, err := f.store.q.UpsertAgentListener(f.ctx, dbsqlc.UpsertAgentListenerParams{
			ProjectID: testProjectID, AgentID: agent.ID, AppID: f.app.ID,
			ListenerKey: "chat__thread_messages", ScopeKind: "thread", ScopeRef: ref, Events: []string{"message"},
			SourceConfigID: agent.CurrentConfigID, ToolCallID: &ids[index], Origin: "runtime",
		})
		require.NoError(t, err)
		listeners = append(listeners, listener)
	}
	return listeners
}

func TestAppListenersSurviveSendingToolEdits(t *testing.T) {
	for _, change := range []string{"remove", "deny", "disable", "destination"} {
		t.Run(change, func(t *testing.T) {
			f := newAppActivationFixture(t)
			listener := f.listener()
			listener.Config = json.RawMessage(`{}`)
			definition := f.withSendingTools(
				t,
				f.definition(t, "Follow replies", map[string]agentconfig.AppCapabilityCompiled{"chat__thread_messages": listener}),
			)
			input := f.launchInput(uuid.Nil, "follow-launch")
			input.DerivedConfig = &definition
			launch, err := f.store.Execution().LaunchAgent(f.ctx, input)
			require.NoError(t, err)
			require.Empty(t, f.listeners(t, launch.Agent.ID))
			follows := f.confirmedFollows(t, launch.Agent)
			var compiled agentconfig.Compiled
			require.NoError(t, json.Unmarshal(definition.CompiledDefinition, &compiled))
			name := "app__chat__post_message"
			tool := compiled.Tools[name]
			switch change {
			case "deny":
				tool.Permission = toolpermission.DefaultSelection(toolpermission.ModeAlwaysDeny)
			case "disable":
				tool.Enabled = false
			case "destination":
				tool.Config = json.RawMessage(`{"channel_id":"C456"}`)
			}
			compiled.Tools[name] = tool
			if change == "remove" {
				delete(compiled.Tools, name)
			}
			update := f.changeInput(t, launch.Agent.ID, "Sender changed", compiled.Listeners, "sender-edit")
			update.CreateAgentConfigInput = f.encodedDefinition(t, compiled)
			changed, err := f.store.Execution().ChangeAgentConfig(f.ctx, update)
			require.NoError(t, err)
			listeners := f.listeners(t, launch.Agent.ID)
			require.Len(t, listeners, 2)
			for _, follow := range follows {
				found := false
				for _, listener := range listeners {
					if listener.ID == follow.ID {
						found = true
						require.Equal(t, follow.ToolCallID, listener.ToolCallID)
						require.Equal(t, changed.AgentConfig.ID, listener.SourceConfigID)
					}
				}
				require.True(t, found)
			}
		})
	}
}

func TestRemovingListenerStopsInitialAndRuntimeSubscriptions(t *testing.T) {
	f := newAppActivationFixture(t)
	definition := f.withSendingTools(
		t,
		f.definition(
			t,
			"Listen and send",
			map[string]agentconfig.AppCapabilityCompiled{"chat__thread_messages": f.listener()},
		),
	)
	input := f.launchInput(uuid.Nil, "listener-launch")
	input.DerivedConfig = &definition
	launch, err := f.store.Execution().LaunchAgent(f.ctx, input)
	require.NoError(t, err)
	f.confirmedFollows(t, launch.Agent)
	require.Len(t, f.listeners(t, launch.Agent.ID), 3)
	var compiled agentconfig.Compiled
	require.NoError(t, json.Unmarshal(definition.CompiledDefinition, &compiled))
	compiled.Listeners = nil
	update := f.changeInput(t, launch.Agent.ID, "Stop receiving", nil, "remove-listener")
	update.CreateAgentConfigInput = f.encodedDefinition(t, compiled)
	_, err = f.store.Execution().ChangeAgentConfig(f.ctx, update)
	require.NoError(t, err)
	require.Empty(t, f.listeners(t, launch.Agent.ID), "sending tools do not recreate subscriptions")
	listener := f.listener()
	listener.Config = json.RawMessage(`{}`)
	restored := f.changeInput(t, launch.Agent.ID, "Permit new follows",
		map[string]agentconfig.AppCapabilityCompiled{"chat__thread_messages": listener}, "restore-listener")
	_, err = f.store.Execution().ChangeAgentConfig(f.ctx, restored)
	require.NoError(t, err)
	require.Empty(t, f.listeners(t, launch.Agent.ID), "restoring an empty listener must not resurrect old follows")
}

func TestAppCapabilitiesProfileLaunchActivationAndReplay(t *testing.T) {
	t.Parallel()
	f := newAppActivationFixture(t)
	resources := map[string]agentconfig.AppCapabilityCompiled{"chat__thread_messages": f.listener()}
	config, err := f.store.Execution().CreateAgentConfig(f.ctx, f.definition(t, "Saved app config", resources))
	require.NoError(t, err)
	profile, err := f.store.Execution().
		CreateAgentProfile(
			f.ctx,
			executionstore.CreateAgentProfileInput{
				ProjectID:       testProjectID,
				Name:            "App profile",
				CurrentConfigID: config.ID,
			},
		)
	require.NoError(t, err)
	var count int
	require.NoError(
		t,
		f.store.pool.QueryRow(f.ctx, `SELECT count(*) FROM agent_listeners WHERE project_id=$1`, testProjectID).
			Scan(&count),
	)
	require.Zero(t, count, "config/profile save must not subscribe")
	input := f.launchInput(config.ID, "app-launch")
	input.ProfileID = profile.ID
	launch, err := f.store.Execution().LaunchAgent(f.ctx, input)
	require.NoError(t, err)
	require.True(t, launch.Created)
	listeners := f.listeners(t, launch.Agent.ID)
	require.Len(t, listeners, 1)
	require.Equal(t, config.ID, listeners[0].SourceConfigID)
	require.Equal(t, "channel", listeners[0].ScopeKind)
	require.Equal(t, "C123", listeners[0].ScopeRef)
	require.Equal(t, []string{"message"}, listeners[0].Events)
	update := f.changeInput(t, launch.Agent.ID, "Unrelated instruction change", resources, "app-edit")
	update.ExpectedCurrentConfigID = config.ID
	changed, err := f.store.Execution().ChangeAgentConfig(f.ctx, update)
	require.NoError(t, err)
	updatedListeners := f.listeners(t, launch.Agent.ID)
	require.Len(t, updatedListeners, 1)
	require.Equal(t, listeners[0].ID, updatedListeners[0].ID)
	require.Equal(t, changed.AgentConfig.ID, updatedListeners[0].SourceConfigID)
	f.disable(t)
	// Removing a revoked authority still locks it, but does not require it active.
	removed, err := f.store.Execution().
		ChangeAgentConfig(f.ctx, f.changeInput(t, launch.Agent.ID, "Remove app", nil, "app-remove"))
	require.NoError(t, err)
	require.Empty(t, f.listeners(t, launch.Agent.ID))
	old, err := f.store.Execution().ChangeAgentConfig(f.ctx, update)
	require.NoError(t, err)
	require.Equal(t, changed.ConfigChange.AgentInput.ID, old.ConfigChange.AgentInput.ID)
	require.Equal(t, changed.ConfigChange.Event.ID, old.ConfigChange.Event.ID)
	replayed, err := f.store.Execution().LaunchAgent(f.ctx, input)
	require.NoError(t, err)
	require.False(t, replayed.Created)
	require.Equal(t, launch.Agent.ID, replayed.Agent.ID)
	require.Equal(t, removed.AgentConfig.ID, replayed.Agent.CurrentConfigID)
	require.Empty(t, f.listeners(t, launch.Agent.ID), "old replays cannot restore subscriptions")
	conflict := update
	conflict.Reason = "different intent"
	_, err = f.store.Execution().ChangeAgentConfig(f.ctx, conflict)
	require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
}

func TestAppCapabilitiesRejectCrossProjectApps(t *testing.T) {
	f := newAppActivationFixture(t)
	otherProject := seedAdditionalProjectForTest(t, f.ctx, f.store.pool, "other-app")
	otherConfig := storagefixture.SeedAgentConfig(
		t,
		f.ctx,
		f.store.Models(),
		f.store.Execution(),
		testOrgID,
		otherProject,
		"instruction: Other project\nmodel: {provider_config: openai-prod, name: gpt-test}\n",
	)
	otherProfile, err := f.store.Execution().
		CreateAgentProfile(
			f.ctx,
			executionstore.CreateAgentProfileInput{
				ProjectID:       otherProject,
				Name:            "Other",
				CurrentConfigID: otherConfig.ID,
			},
		)
	require.NoError(t, err)
	credential := createIntegrationCredential(t, f.ctx, f.store, otherProject, f.user.ID, "other-app")
	otherInput := slackProjectAppSetupInput(
		otherProfile.ID,
		uuid.Nil,
		f.user.ID,
		credential,
		"A_OTHER",
		"T_OTHER",
	)
	otherInput.ProjectID = otherProject
	other := mustCreateProjectApp(t, f.ctx, f.store, otherInput)
	base, err := f.store.Execution().LaunchAgent(f.ctx, f.launchInput(f.profile.CurrentConfigID, "app-base"))
	require.NoError(t, err)
	for _, test := range []struct {
		name string
		app  uuid.UUID
		want error
	}{
		{"active", other.ID, storeerr.ErrNotFound},
		{"disconnected", other.ID, storeerr.ErrNotFound},
		{"deleted", other.ID, storeerr.ErrNotFound},
		{"missing", uuid.New(), storeerr.ErrNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.name == "disconnected" {
				applied, err := f.store.Integrations().DisconnectProjectApp(f.ctx, integrationstore.DisconnectProjectAppInput{
					ProjectID: otherProject, AppID: other.ID,
				})
				require.NoError(t, err)
				require.True(t, applied)
			}
			if test.name == "deleted" {
				require.NoError(t, f.store.Integrations().DeleteProjectApp(f.ctx, testOrgID, otherProject, other.ID))
			}
			resource := f.listener()
			resource.AppID = publicResourceID(publicid.KindProjectApp, test.app)
			resources := map[string]agentconfig.AppCapabilityCompiled{"chat__thread_messages": resource}
			definition := f.definition(t, test.name, resources)
			input := f.launchInput(uuid.Nil, test.name)
			input.DerivedConfig = &definition
			_, err := f.store.Execution().LaunchAgent(f.ctx, input)
			require.ErrorIs(t, err, test.want)
			_, err = f.store.Execution().
				ChangeAgentConfig(f.ctx, f.changeInput(t, base.Agent.ID, test.name, resources, "change-"+test.name))
			require.ErrorIs(t, err, test.want)
			current, err := f.store.Execution().GetAgentInProject(f.ctx, testProjectID, base.Agent.ID)
			require.NoError(t, err)
			require.Equal(t, base.Agent.CurrentConfigID, current.CurrentConfigID)
			require.Empty(t, f.listeners(t, base.Agent.ID))
		})
	}
}

func TestAppCapabilitiesListenerQuotaRollsBackLaunchAndActivation(t *testing.T) {
	t.Parallel()
	f := newAppActivationFixture(t)
	base, err := f.store.Execution().LaunchAgent(f.ctx, f.launchInput(f.profile.CurrentConfigID, "quota-base"))
	require.NoError(t, err)
	_, err = f.store.pool.Exec(
		f.ctx,
		`INSERT INTO org_resource_limit_overrides(org_id,max_active_app_listeners_per_agent) VALUES($1,0)`,
		testOrgID,
	)
	require.NoError(t, err)
	resources := map[string]agentconfig.AppCapabilityCompiled{"chat__thread_messages": f.listener()}
	definition := f.definition(t, "Quota should roll back", resources)
	input := f.launchInput(uuid.Nil, "quota-derived")
	input.DerivedConfig = &definition
	_, err = f.store.Execution().LaunchAgent(f.ctx, input)
	require.ErrorIs(t, err, storeerr.ErrConflict)
	_, err = f.store.Execution().
		ChangeAgentConfig(f.ctx, f.changeInput(t, base.Agent.ID, "Quota should roll back", resources, "quota-change"))
	require.ErrorIs(t, err, storeerr.ErrConflict)
	for _, check := range []struct{ query, value string }{
		{
			`SELECT count(*) FROM agent_configs WHERE project_id=$1 AND effective_definition_hash=$2`,
			definition.EffectiveDefinitionHash,
		},
		{`SELECT count(*) FROM agents WHERE project_id=$1 AND idempotency_key=$2`, input.IdempotencyKey},
	} {
		var count int
		require.NoError(t, f.store.pool.QueryRow(f.ctx, check.query, testProjectID, check.value).Scan(&count))
		require.Zero(t, count)
	}
	current, err := f.store.Execution().GetAgentInProject(f.ctx, testProjectID, base.Agent.ID)
	require.NoError(t, err)
	require.Equal(t, base.Agent.CurrentConfigID, current.CurrentConfigID)
	require.Empty(t, f.listeners(t, base.Agent.ID))
}

func TestAppCapabilitiesFailedActivationPreservesExistingListeners(t *testing.T) {
	t.Parallel()
	f := newAppActivationFixture(t)
	resources := map[string]agentconfig.AppCapabilityCompiled{"chat__thread_messages": f.listener()}
	definition := f.definition(t, "Existing listener", resources)
	input := f.launchInput(uuid.Nil, "existing-listener")
	input.DerivedConfig = &definition
	launch, err := f.store.Execution().LaunchAgent(f.ctx, input)
	require.NoError(t, err)
	before := f.listeners(t, launch.Agent.ID)
	require.Len(t, before, 1)
	_, err = f.store.pool.Exec(
		f.ctx,
		`INSERT INTO org_resource_limit_overrides(org_id,max_active_app_listeners_per_agent) VALUES($1,1)`,
		testOrgID,
	)
	require.NoError(t, err)
	second := f.listener()
	second.Config = json.RawMessage(`{"conversations":[{"channel_id":"C123"},{"channel_id":"C456"}]}`)
	resources["chat__thread_messages"] = second
	change := f.changeInput(t, launch.Agent.ID, "Exceeds listener limit", resources, "failed-listener-edit")
	_, err = f.store.Execution().ChangeAgentConfig(f.ctx, change)
	require.ErrorIs(t, err, storeerr.ErrConflict)
	require.Equal(
		t,
		before,
		f.listeners(t, launch.Agent.ID),
		"rollback must restore listener identity, authority and timestamps",
	)
	current, err := f.store.Execution().GetAgentInProject(f.ctx, testProjectID, launch.Agent.ID)
	require.NoError(t, err)
	require.Equal(t, launch.Agent.CurrentConfigID, current.CurrentConfigID)
	var count int
	require.NoError(
		t,
		f.store.pool.QueryRow(
			f.ctx,
			`SELECT count(*) FROM agent_inputs WHERE project_id=$1 AND agent_id=$2 AND input_idempotency_key=$3`,
			testProjectID,
			launch.Agent.ID,
			change.IdempotencyKey,
		).
			Scan(
				&count,
			),
	)
	require.Zero(t, count, "failed activation must not retain its input or event")
}

func TestAppCapabilitiesLockAppsBeforeLaunchKeyAndProfile(t *testing.T) {
	t.Parallel()
	f := newAppActivationFixture(t)
	definition := f.definition(
		t,
		"Lock order launch",
		map[string]agentconfig.AppCapabilityCompiled{"chat__thread_messages": f.listener()},
	)
	input := f.launchInput(uuid.Nil, "lock-order-launch")
	input.DerivedConfig, input.ProfileID = &definition, f.profile.ID
	input.DerivedBaseConfigID = f.profile.CurrentConfigID
	control := integrationdb.BeginTx(t, f.ctx, f.store.pool)
	q := dbsqlc.New(control)
	require.NoError(
		t,
		q.LockProjectAppLifecycleExclusive(
			f.ctx,
			dbsqlc.LockProjectAppLifecycleExclusiveParams{AppID: f.app.ID},
		),
	)
	done := integrationdb.RunAsync(func() (executionstore.LaunchAgentResult, error) {
		return f.store.Execution().IntegrationLaunchAgentOnce(f.ctx, input)
	})
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.store.pool, "LockProjectAppLifecycleShared", 1)
	lockCtx, cancel := context.WithTimeout(f.ctx, 2*time.Second)
	defer cancel()
	require.NoError(
		t,
		q.LockAgentLaunchIdempotencyKey(
			lockCtx,
			dbsqlc.LockAgentLaunchIdempotencyKeyParams{ProjectID: testProjectID, IdempotencyKey: input.IdempotencyKey},
		),
	)
	_, err := q.LockAgentProfile(
		lockCtx,
		dbsqlc.LockAgentProfileParams{ProjectID: testProjectID, ProfileID: f.profile.ID},
	)
	require.NoError(t, err, "launch must not hold its profile while waiting on a app")
	require.NoError(t, control.Commit(f.ctx))
	launch := integrationdb.AwaitSuccess(t, done, "launch after app gate")
	require.Len(t, f.listeners(t, launch.Agent.ID), 1)
}

func TestAppCapabilitiesLockAppsBeforeAgentSourcesAndAgent(t *testing.T) {
	t.Parallel()
	f := newAppActivationFixture(t)
	launch, err := f.store.Execution().LaunchAgent(f.ctx, f.launchInput(f.profile.CurrentConfigID, "lock-order-base"))
	require.NoError(t, err)
	input := f.changeInput(
		t,
		launch.Agent.ID,
		"Lock order change",
		map[string]agentconfig.AppCapabilityCompiled{"chat__thread_messages": f.listener()},
		"lock-order-change",
	)
	control := integrationdb.BeginTx(t, f.ctx, f.store.pool)
	q := dbsqlc.New(control)
	require.NoError(
		t,
		q.LockProjectAppLifecycleExclusive(
			f.ctx,
			dbsqlc.LockProjectAppLifecycleExclusiveParams{AppID: f.app.ID},
		),
	)
	done := integrationdb.RunAsync(func() (executionstore.ChangeAgentConfigResult, error) {
		return f.store.Execution().IntegrationChangeAgentConfigOnce(f.ctx, input)
	})
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.store.pool, "LockProjectAppLifecycleShared", 1)
	lockCtx, cancel := context.WithTimeout(f.ctx, 2*time.Second)
	defer cancel()
	require.NoError(
		t,
		q.LockAgentMachineSources(lockCtx, dbsqlc.LockAgentMachineSourcesParams{AgentID: launch.Agent.ID}),
	)
	_, err = q.LockAgentInProject(
		lockCtx,
		dbsqlc.LockAgentInProjectParams{ProjectID: testProjectID, ID: launch.Agent.ID},
	)
	require.NoError(t, err, "activation must not hold agent locks while waiting on a app")
	require.NoError(t, control.Commit(f.ctx))
	changed := integrationdb.AwaitSuccess(t, done, "change after app gate")
	require.Equal(t, changed.AgentConfig.ID, f.listeners(t, launch.Agent.ID)[0].SourceConfigID)
}

func TestAppCapabilitiesRetryWhenCurrentConfigChangesDuringAppWait(t *testing.T) {
	t.Parallel()
	f := newAppActivationFixture(t)
	launch, err := f.store.Execution().LaunchAgent(f.ctx, f.launchInput(f.profile.CurrentConfigID, "retry-base"))
	require.NoError(t, err)
	input := f.changeInput(
		t,
		launch.Agent.ID,
		"Waiting change",
		map[string]agentconfig.AppCapabilityCompiled{"chat__thread_messages": f.listener()},
		"waiting-change",
	)
	control := integrationdb.BeginTx(t, f.ctx, f.store.pool)
	require.NoError(
		t,
		dbsqlc.New(control).
			LockProjectAppLifecycleExclusive(
				f.ctx,
				dbsqlc.LockProjectAppLifecycleExclusiveParams{AppID: f.app.ID},
			),
	)
	done := integrationdb.RunAsync(func() (executionstore.ChangeAgentConfigResult, error) {
		return f.store.Execution().IntegrationChangeAgentConfigOnce(f.ctx, input)
	})
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.store.pool, "LockProjectAppLifecycleShared", 1)
	_, err = f.store.Execution().
		ChangeAgentConfig(f.ctx, f.changeInput(t, launch.Agent.ID, "Concurrent edit", nil, "concurrent-edit"))
	require.NoError(t, err)
	require.NoError(t, control.Commit(f.ctx))
	outcome := integrationdb.Await(t, done, "stale app discovery")
	require.ErrorIs(t, outcome.Err, storeutil.ErrRetryTransaction)
	changed, err := f.store.Execution().ChangeAgentConfig(f.ctx, input)
	require.NoError(t, err)
	require.Equal(t, changed.AgentConfig.ID, f.listeners(t, launch.Agent.ID)[0].SourceConfigID)
}

func TestAppCapabilitiesUnavailableSecondaryDoesNotBlockLaunchOrConfigChange(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"disconnected", "deleted"} {
		for _, withListener := range []bool{false, true} {
			name := state + "/disabled-tool"
			if withListener {
				name = state + "/listener"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				f := newAppActivationFixture(t)
				secondary := (appInteractionFixture{ctx: f.ctx, store: f.store, user: f.user}).createApp(t, "secondary")
				primaryListener := f.listener()
				secondaryListener := primaryListener
				secondaryListener.AppID = publicResourceID(publicid.KindProjectApp, secondary.ID)
				listeners := map[string]agentconfig.AppCapabilityCompiled{"chat__thread_messages": primaryListener}
				if withListener {
					listeners["secondary__thread_messages"] = secondaryListener
				}
				definition := f.definition(t, "Saved before app revocation", listeners)
				var compiled agentconfig.Compiled
				require.NoError(t, json.Unmarshal(definition.CompiledDefinition, &compiled))
				if compiled.Tools == nil {
					compiled.Tools = map[string]agentconfig.ToolCompiled{}
				}
				compiled.Tools["app__secondary__post_message"] = agentconfig.ToolCompiled{
					Enabled: false, AppID: secondaryListener.AppID, Config: json.RawMessage(`{"channel_id":"C123"}`),
					Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow),
				}
				config, err := f.store.Execution().CreateAgentConfig(f.ctx, f.encodedDefinition(t, compiled))
				require.NoError(t, err)
				base, err := f.store.Execution().LaunchAgent(f.ctx, f.launchInput(f.profile.CurrentConfigID, "base"))
				require.NoError(t, err)
				if state == "deleted" {
					require.NoError(t, f.store.Integrations().DeleteProjectApp(f.ctx, testOrgID, testProjectID, secondary.ID))
				} else {
					applied, err := f.store.Integrations().DisconnectProjectApp(f.ctx, integrationstore.DisconnectProjectAppInput{
						ProjectID: testProjectID, AppID: secondary.ID,
					})
					require.NoError(t, err)
					require.True(t, applied)
				}
				launch, err := f.store.Execution().LaunchAgent(f.ctx, f.launchInput(config.ID, "after-revocation"))
				require.NoError(t, err)
				compiled.Instruction = "Unrelated instruction edit after revocation"
				change := f.changeInput(t, base.Agent.ID, compiled.Instruction, listeners, "edit-after-revocation")
				change.CreateAgentConfigInput = f.encodedDefinition(t, compiled)
				changed, err := f.store.Execution().ChangeAgentConfig(f.ctx, change)
				require.NoError(t, err)
				wantCount := 1
				if withListener && state == "disconnected" {
					wantCount = 2
				}
				for _, agent := range []struct{ id, config uuid.UUID }{
					{launch.Agent.ID, config.ID}, {base.Agent.ID, changed.AgentConfig.ID},
				} {
					rows := f.listeners(t, agent.id)
					require.Len(t, rows, wantCount)
					for _, row := range rows {
						require.Equal(t, agent.config, row.SourceConfigID)
					}
				}
				matching := func(appID uuid.UUID) []dbsqlc.AgentListener {
					t.Helper()
					rows, err := f.store.q.ListMatchingAgentListeners(f.ctx, dbsqlc.ListMatchingAgentListenersParams{
						ProjectID: testProjectID, AppID: appID, Event: "message",
						Scopes: json.RawMessage(`[{"kind":"channel","ref":"C123"}]`),
					})
					require.NoError(t, err)
					return rows
				}
				require.Len(t, matching(f.app.ID), 2, "unrelated active app retains routing")
				require.Empty(t, matching(secondary.ID), "unavailable app listeners are inert")
				// Required origin/receipt apps cannot use the relaxed metadata rule.
				tx := integrationdb.BeginTx(t, f.ctx, f.store.pool)
				wantErr := storeerr.ErrUnauthorized
				if state == "deleted" {
					wantErr = storeerr.ErrNotFound
				}
				err = integrationstore.LockAppsTx(f.ctx, tx, testProjectID,
					[]string{secondaryListener.AppID}, secondary.ID)
				require.ErrorIs(t, err, wantErr)
				require.NoError(t, tx.Rollback(f.ctx))
				if withListener {
					// Exercise the runtime mutation guard directly after acquiring
					// the gates, so its own active-state check is covered.
					tx = integrationdb.BeginTx(t, f.ctx, f.store.pool)
					q := dbsqlc.New(tx)
					require.NoError(
						t,
						q.LockProjectAppLifecycleShared(f.ctx, dbsqlc.LockProjectAppLifecycleSharedParams{AppID: secondary.ID}),
					)
					_, err = q.LockAgentInProject(f.ctx, dbsqlc.LockAgentInProjectParams{ProjectID: testProjectID, ID: base.Agent.ID})
					require.NoError(t, err)
					err = integrationstore.RegisterRuntimeListenerTx(f.ctx, tx, integrationstore.RegisterRuntimeListenerInput{
						OrgID: testOrgID, ProjectID: testProjectID, AgentID: base.Agent.ID, ConfigID: changed.AgentConfig.ID,
						AppID: secondary.ID, ListenerKey: "secondary__thread_messages", Capability: secondaryListener,
						Address: integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:111.222"},
					})
					require.ErrorIs(t, err, wantErr)
					require.NoError(t, tx.Rollback(f.ctx))
				}
				if state == "disconnected" {
					current, err := f.store.Integrations().GetProjectApp(f.ctx, testProjectID, secondary.ID)
					require.NoError(t, err)
					credential, err := f.store.Secrets().GetSecret(f.ctx, testOrgID, current.CredentialSecretID)
					require.NoError(t, err)
					_, err = f.store.Integrations().ConfigureProjectApp(f.ctx, integrationstore.ConfigureProjectAppInput{
						OrgID:                 testOrgID,
						ProjectID:             testProjectID,
						AppID:                 current.ID,
						InstalledByUserID:     f.user.ID,
						Provider:              current.Provider,
						ProviderTenantID:      current.ProviderTenantID,
						ProviderAccountRef:    current.ProviderAccountRef,
						CredentialSecretID:    current.CredentialSecretID,
						CredentialVersionID:   credential.CurrentVersionID,
						ExpectedSetupRevision: current.SetupRevision,
						OAuthFlowID:           uuid.Must(uuid.NewV7()),
						ProviderIdentity:      current.ProviderIdentity,
					})
					require.NoError(t, err)
					if withListener {
						require.Len(t, matching(secondary.ID), 2, "reconnect activates materialized configured listeners")
					} else {
						require.Empty(t, matching(secondary.ID), "disabled tools cannot create listeners")
					}
				}
			})
		}
	}
}

func TestAppCapabilitiesInboxLaunchToleratesUnavailableSecondary(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"disconnected", "deleted"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			f := newInboxLaunchFixture(t, false, time.Minute, "a")
			secondary := (appInteractionFixture{ctx: f.ctx, store: f.store, user: f.user}).createApp(t, "secondary")
			listener := f.listener()
			listener.AppID = publicResourceID(publicid.KindProjectApp, secondary.ID)
			definition := f.definition(t, "Secondary app is optional", map[string]agentconfig.AppCapabilityCompiled{
				"chat__thread_messages": f.listener(), "secondary__thread_messages": listener,
			})
			slot := f.slots["a"]
			slot.AgentID = uuid.Must(uuid.NewV7())
			slot.Selection.Address.Ref = "C123:789.012"
			slot.Launch.InitialInput.Origin.Address = slot.Selection.Address
			slot.Launch.InitialInput.SemanticEventKey = "message:789.012"
			slot.Launch.IdempotencyKey = "unavailable-secondary"
			slot.Launch.DerivedConfig = &definition
			_, _, err := f.store.Integrations().AcceptIntegrationReceipt(f.ctx, integrationstore.VerifiedIntegrationReceipt{
				ProjectID: testProjectID, AppID: f.app.ID, ReceiptKey: "unavailable-secondary", Payload: []byte(`{}`),
			})
			require.NoError(t, err)
			receipt, found, err := f.store.Integrations().
				ClaimIntegrationInbox(f.ctx, integrationstore.ClaimIntegrationInboxInput{
					ProjectID: testProjectID, AppID: f.app.ID, LeaseDuration: time.Minute,
				})
			require.NoError(t, err)
			require.True(t, found)
			plan, err := json.Marshal(map[string]executionstore.InboxLaunchSlot{"a": slot})
			require.NoError(t, err)
			require.NoError(t, f.store.Integrations().WithIntegrationInboxLease(f.ctx, receipt.Lease(),
				func(work *integrationstore.IntegrationInboxLeaseTx) error { return work.FreezePlan(f.ctx, plan) }))
			if state == "deleted" {
				require.NoError(t, f.store.Integrations().DeleteProjectApp(f.ctx, testOrgID, testProjectID, secondary.ID))
			} else {
				applied, err := f.store.Integrations().DisconnectProjectApp(f.ctx, integrationstore.DisconnectProjectAppInput{
					ProjectID: testProjectID, AppID: secondary.ID,
				})
				require.NoError(t, err)
				require.True(t, applied)
			}
			launch, err := f.store.Execution().AdmitInboxLaunchSlot(f.ctx, receipt.Lease(), "a")
			require.NoError(t, err)
			require.True(t, launch.Created)
			require.Equal(t, slot.AgentID, launch.Agent.ID)
			// Launch also follows the selected conversation under the primary app.
			wantListeners := 2
			if state == "disconnected" {
				wantListeners = 3
			}
			require.Len(t, f.listeners(t, launch.Agent.ID), wantListeners)
			replay, err := f.store.Execution().AdmitInboxLaunchSlot(f.ctx, receipt.Lease(), "a")
			require.NoError(t, err)
			require.False(t, replay.Created)
			require.Equal(t, launch.Agent.ID, replay.Agent.ID)
		})
	}
}
