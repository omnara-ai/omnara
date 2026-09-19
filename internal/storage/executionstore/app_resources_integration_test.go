//go:build integration

package executionstore_test

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
	ctx        context.Context //nolint:containedctx // Shared test fixture retains only its test-lifetime context.
	store      *Store
	user       identitystore.UserRecord
	profile    executionstore.AgentProfileRecord
	connection integrationstore.IntegrationConnectionRecord
}

func newAppActivationFixture(t *testing.T) appActivationFixture {
	t.Helper()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	user := createIntegrationProjectAdmin(t, ctx, store, "app-activation@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, "app-activation")
	credential := createIntegrationCredential(t, ctx, store, testProjectID, user.ID, "app-activation")
	connection := mustCreateIntegrationConnection(
		t,
		ctx,
		store,
		slackIntegrationConnectionInput(profile.ID, uuid.Nil, user.ID, credential, "A_ACTIVATION", "T_ACTIVATION"),
	)
	return appActivationFixture{ctx: ctx, store: store, user: user, profile: profile, connection: connection}
}

func (f appActivationFixture) resource() agentconfig.AppResourceCompiled {
	return agentconfig.AppResourceCompiled{
		Definition: appdefinition.Slack, Enabled: true,
		ConnectionID: publicResourceID(publicid.KindIntegrationConnection, f.connection.ID),
		Scope:        &appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: "C123"}},
		Listener:     &appdefinition.Listener{Events: []string{"message"}},
		Follow:       &appdefinition.Follow{Replies: true},
	}
}

func (f appActivationFixture) definition(
	t *testing.T,
	instruction string,
	resources map[string]agentconfig.AppResourceCompiled,
) executionstore.CreateAgentConfigInput {
	t.Helper()
	var compiled agentconfig.Compiled
	require.NoError(t, json.Unmarshal(f.profile.CurrentConfig.CompiledDefinition, &compiled))
	compiled.Instruction, compiled.AppResources = instruction, resources
	for key, resource := range resources {
		if !resource.Enabled {
			continue
		}
		for _, name := range resource.Tools {
			compiled.Tools[name] = agentconfig.ToolCompiled{
				Enabled:    true,
				Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow),
				AppOrigin:  &agentconfig.AppToolOrigin{ResourceKeys: []string{key}},
			}
		}
	}
	encoded, err := agentconfig.EncodeCompiled(compiled)
	require.NoError(t, err)
	return executionstore.CreateAgentConfigInput{
		ProjectID:               testProjectID,
		ConfiguredModelID:       f.profile.CurrentConfig.ConfiguredModelID,
		CompiledDefinition:      encoded.CanonicalJSON,
		CompilerVersion:         agentconfig.CompilerVersion,
		EffectiveDefinitionHash: encoded.Hash,
	}
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
	resources map[string]agentconfig.AppResourceCompiled,
	key string,
) executionstore.ChangeAgentConfigInput {
	return executionstore.ChangeAgentConfigInput{
		CreateAgentConfigInput: f.definition(t, instruction, resources),
		AgentID:                agentID,
		ActorType:              identitystore.PrincipalTypeUser,
		ActorID:                f.user.ID,
		Reason:                 "app-resource-test",
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
	flow := f.connection.LastOAuthFlowID
	changed, err := f.store.Integrations().
		DisableIntegrationConnection(
			f.ctx,
			integrationstore.DisableIntegrationConnectionInput{
				ProjectID:           testProjectID,
				ID:                  f.connection.ID,
				ExpectedOAuthFlowID: &flow,
			},
		)
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
			ToolName: toolcatalog.ToolNameSlackPostMessage,
			ToolType: toolcatalog.ToolTypeBuiltIn,
			Allowed:  true,
			Input:    json.RawMessage(`{"text":"First thread","follow_replies":true}`),
		},
		{
			TestName: "follow-two",
			ToolName: toolcatalog.ToolNameSlackPostMessage,
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
			ProjectID: testProjectID, AgentID: agent.ID, ConnectionID: f.connection.ID,
			ResourceKey: "chat", ScopeKind: "thread", ScopeRef: ref, Events: []string{"message"},
			SourceConfigID: agent.CurrentConfigID, ToolCallID: &ids[index],
		})
		require.NoError(t, err)
		listeners = append(listeners, listener)
	}
	return listeners
}

func TestAppResourcesPreserveFollowsAndNarrowToExactConversation(t *testing.T) {
	t.Parallel()
	f := newAppActivationFixture(t)
	resource := f.resource()
	resource.Listener = nil
	resource.Tools = []string{toolcatalog.ToolNameSlackPostMessage}
	resources := map[string]agentconfig.AppResourceCompiled{"chat": resource}
	definition := f.definition(t, "Follow-only app", resources)
	input := f.launchInput(uuid.Nil, "follow-launch")
	input.DerivedConfig = &definition
	launch, err := f.store.Execution().LaunchAgent(f.ctx, input)
	require.NoError(t, err)
	require.Empty(t, f.listeners(t, launch.Agent.ID), "follow policy alone is not a listener")
	follows := f.confirmedFollows(t, launch.Agent)
	changed, err := f.store.Execution().
		ChangeAgentConfig(f.ctx, f.changeInput(t, launch.Agent.ID, "Instruction edit", resources, "follow-edit"))
	require.NoError(t, err)
	listeners := f.listeners(t, launch.Agent.ID)
	require.Len(t, listeners, 2)
	for _, follow := range follows {
		found := false
		for _, listener := range listeners {
			if listener.ID == follow.ID {
				found = true
				require.Equal(t, follow.ToolCallID, listener.ToolCallID)
				require.Equal(t, follow.ScopeRef, listener.ScopeRef)
				require.Equal(t, follow.Events, listener.Events)
				require.Equal(t, changed.AgentConfig.ID, listener.SourceConfigID)
			}
		}
		require.True(t, found, "instruction edit dropped follow %s", follow.ID)
	}
	resource.Scope = &appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: "C123", ThreadTS: "111.222"}}
	resources["chat"] = resource
	narrowed, err := f.store.Execution().
		ChangeAgentConfig(f.ctx, f.changeInput(t, launch.Agent.ID, "Narrow to one thread", resources, "follow-narrow"))
	require.NoError(t, err)
	listeners = f.listeners(t, launch.Agent.ID)
	require.Len(t, listeners, 1)
	require.Equal(t, follows[0].ID, listeners[0].ID)
	require.Equal(t, narrowed.AgentConfig.ID, listeners[0].SourceConfigID)
	resource.Scope = f.resource().Scope
	resources["chat"] = resource
	_, err = f.store.Execution().
		ChangeAgentConfig(f.ctx, f.changeInput(t, launch.Agent.ID, "Widen back to channel", resources, "follow-widen"))
	require.NoError(t, err)
	listeners = f.listeners(t, launch.Agent.ID)
	require.Len(t, listeners, 1, "widening must not resurrect previously removed follows")
	require.Equal(t, follows[0].ID, listeners[0].ID)
}

func TestAppResourcesRemoveFollowAuthority(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{
		"remove-resource", "disable-resource", "remove-follow", "change-scope", "change-connection",
	} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f := newAppActivationFixture(t)
			resource := f.resource()
			resource.Tools = []string{toolcatalog.ToolNameSlackPostMessage}
			resource.Listener = nil
			resources := map[string]agentconfig.AppResourceCompiled{"chat": resource}
			definition := f.definition(t, "Initial follow authority", resources)
			input := f.launchInput(uuid.Nil, "follow-launch")
			input.DerivedConfig = &definition
			launch, err := f.store.Execution().LaunchAgent(f.ctx, input)
			require.NoError(t, err)
			f.confirmedFollows(t, launch.Agent)
			switch mode {
			case "disable-resource":
				resource.Enabled = false
			case "remove-follow":
				resource.Follow = nil
			case "change-scope":
				resource.Scope = &appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: "C456"}}
			case "change-connection":
				credential := createIntegrationCredential(t, f.ctx, f.store, testProjectID, f.user.ID, "second-follow")
				other := mustCreateIntegrationConnection(
					t,
					f.ctx,
					f.store,
					slackIntegrationConnectionInput(
						f.profile.ID,
						uuid.Nil,
						f.user.ID,
						credential,
						"A_SECOND",
						"T_SECOND",
					),
				)
				resource.ConnectionID = publicResourceID(publicid.KindIntegrationConnection, other.ID)
			}
			resources["chat"] = resource
			if mode == "remove-resource" {
				resources = nil
			}
			_, err = f.store.Execution().
				ChangeAgentConfig(
					f.ctx,
					f.changeInput(t, launch.Agent.ID, "Remove follow authority", resources, "follow-remove"),
				)
			require.NoError(t, err)
			require.Empty(t, f.listeners(t, launch.Agent.ID))
			// Restoring authority is not a new confirmation of an old follow.
			resource = f.resource()
			resource.Listener = nil
			resource.Tools = []string{toolcatalog.ToolNameSlackPostMessage}
			_, err = f.store.Execution().
				ChangeAgentConfig(
					f.ctx,
					f.changeInput(
						t,
						launch.Agent.ID,
						"Restore policy",
						map[string]agentconfig.AppResourceCompiled{"chat": resource},
						"follow-restore",
					),
				)
			require.NoError(t, err)
			require.Empty(t, f.listeners(t, launch.Agent.ID))
		})
	}
}

func TestAppResourcesProfileLaunchActivationAndReplay(t *testing.T) {
	t.Parallel()
	f := newAppActivationFixture(t)
	resources := map[string]agentconfig.AppResourceCompiled{"chat": f.resource()}
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

func TestAppResourcesRejectRevokedAndCrossProjectConnections(t *testing.T) {
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
	otherInput := slackIntegrationConnectionInput(
		otherProfile.ID,
		uuid.Nil,
		f.user.ID,
		credential,
		"A_OTHER",
		"T_OTHER",
	)
	otherInput.ProjectID = otherProject
	other := mustCreateIntegrationConnection(t, f.ctx, f.store, otherInput)
	base, err := f.store.Execution().LaunchAgent(f.ctx, f.launchInput(f.profile.CurrentConfigID, "app-base"))
	require.NoError(t, err)
	for _, test := range []struct {
		name       string
		connection uuid.UUID
		want       error
	}{
		{"cross-project", other.ID, storeerr.ErrNotFound},
		{"revoked", f.connection.ID, storeerr.ErrUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.name == "revoked" {
				f.disable(t)
			}
			resource := f.resource()
			resource.ConnectionID = publicResourceID(publicid.KindIntegrationConnection, test.connection)
			resources := map[string]agentconfig.AppResourceCompiled{"chat": resource}
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

func TestAppResourcesListenerQuotaRollsBackLaunchAndActivation(t *testing.T) {
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
	resources := map[string]agentconfig.AppResourceCompiled{"chat": f.resource()}
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

func TestAppResourcesFailedActivationPreservesExistingListeners(t *testing.T) {
	t.Parallel()
	f := newAppActivationFixture(t)
	resources := map[string]agentconfig.AppResourceCompiled{"chat": f.resource()}
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
	second := f.resource()
	second.Scope = &appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: "C456"}}
	resources["other"] = second
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

func TestAppResourcesLockConnectionsBeforeLaunchKeyAndProfile(t *testing.T) {
	t.Parallel()
	f := newAppActivationFixture(t)
	definition := f.definition(t, "Lock order launch", map[string]agentconfig.AppResourceCompiled{"chat": f.resource()})
	input := f.launchInput(uuid.Nil, "lock-order-launch")
	input.DerivedConfig, input.ProfileID = &definition, f.profile.ID
	input.DerivedBaseConfigID = f.profile.CurrentConfigID
	control := integrationdb.BeginTx(t, f.ctx, f.store.pool)
	q := dbsqlc.New(control)
	require.NoError(
		t,
		q.LockIntegrationConnectionLifecycleExclusive(
			f.ctx,
			dbsqlc.LockIntegrationConnectionLifecycleExclusiveParams{ConnectionID: f.connection.ID},
		),
	)
	done := integrationdb.RunAsync(func() (executionstore.LaunchAgentResult, error) {
		return f.store.Execution().IntegrationLaunchAgentOnce(f.ctx, input)
	})
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.store.pool, "LockIntegrationConnectionLifecycleShared", 1)
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
	require.NoError(t, err, "launch must not hold its profile while waiting on a connection")
	require.NoError(t, control.Commit(f.ctx))
	launch := integrationdb.AwaitSuccess(t, done, "launch after connection gate")
	require.Len(t, f.listeners(t, launch.Agent.ID), 1)
}

func TestAppResourcesLockConnectionsBeforeAgentSourcesAndAgent(t *testing.T) {
	t.Parallel()
	f := newAppActivationFixture(t)
	launch, err := f.store.Execution().LaunchAgent(f.ctx, f.launchInput(f.profile.CurrentConfigID, "lock-order-base"))
	require.NoError(t, err)
	input := f.changeInput(
		t,
		launch.Agent.ID,
		"Lock order change",
		map[string]agentconfig.AppResourceCompiled{"chat": f.resource()},
		"lock-order-change",
	)
	control := integrationdb.BeginTx(t, f.ctx, f.store.pool)
	q := dbsqlc.New(control)
	require.NoError(
		t,
		q.LockIntegrationConnectionLifecycleExclusive(
			f.ctx,
			dbsqlc.LockIntegrationConnectionLifecycleExclusiveParams{ConnectionID: f.connection.ID},
		),
	)
	done := integrationdb.RunAsync(func() (executionstore.ChangeAgentConfigResult, error) {
		return f.store.Execution().IntegrationChangeAgentConfigOnce(f.ctx, input)
	})
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.store.pool, "LockIntegrationConnectionLifecycleShared", 1)
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
	require.NoError(t, err, "activation must not hold agent locks while waiting on a connection")
	require.NoError(t, control.Commit(f.ctx))
	changed := integrationdb.AwaitSuccess(t, done, "change after connection gate")
	require.Equal(t, changed.AgentConfig.ID, f.listeners(t, launch.Agent.ID)[0].SourceConfigID)
}

func TestAppResourcesRetryWhenCurrentConfigChangesDuringConnectionWait(t *testing.T) {
	t.Parallel()
	f := newAppActivationFixture(t)
	launch, err := f.store.Execution().LaunchAgent(f.ctx, f.launchInput(f.profile.CurrentConfigID, "retry-base"))
	require.NoError(t, err)
	input := f.changeInput(
		t,
		launch.Agent.ID,
		"Waiting change",
		map[string]agentconfig.AppResourceCompiled{"chat": f.resource()},
		"waiting-change",
	)
	control := integrationdb.BeginTx(t, f.ctx, f.store.pool)
	require.NoError(
		t,
		dbsqlc.New(control).
			LockIntegrationConnectionLifecycleExclusive(
				f.ctx,
				dbsqlc.LockIntegrationConnectionLifecycleExclusiveParams{ConnectionID: f.connection.ID},
			),
	)
	done := integrationdb.RunAsync(func() (executionstore.ChangeAgentConfigResult, error) {
		return f.store.Execution().IntegrationChangeAgentConfigOnce(f.ctx, input)
	})
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.store.pool, "LockIntegrationConnectionLifecycleShared", 1)
	_, err = f.store.Execution().
		ChangeAgentConfig(f.ctx, f.changeInput(t, launch.Agent.ID, "Concurrent edit", nil, "concurrent-edit"))
	require.NoError(t, err)
	require.NoError(t, control.Commit(f.ctx))
	outcome := integrationdb.Await(t, done, "stale connection discovery")
	require.ErrorIs(t, outcome.Err, storeutil.ErrRetryTransaction)
	changed, err := f.store.Execution().ChangeAgentConfig(f.ctx, input)
	require.NoError(t, err)
	require.Equal(t, changed.AgentConfig.ID, f.listeners(t, launch.Agent.ID)[0].SourceConfigID)
}
