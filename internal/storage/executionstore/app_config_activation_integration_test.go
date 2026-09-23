//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
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

func (f appActivationFixture) attachment() integrationstore.AppSubscriptionAttachment {
	return integrationstore.AppSubscriptionAttachment{
		AppID: f.app.ID, Type: "thread_messages", Conversation: json.RawMessage(`{"channel_id":"C123"}`),
	}
}

func (f appActivationFixture) definition(
	t *testing.T,
	instruction string,
) executionstore.CreateAgentConfigInput {
	t.Helper()
	var compiled agentconfig.Compiled
	require.NoError(t, json.Unmarshal(f.profile.CurrentConfig.CompiledDefinition, &compiled))
	compiled.Instruction = instruction
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
		CompiledDefinition: encoded.CanonicalJSON, EffectiveDefinitionHash: encoded.Hash,
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
			Enabled: true, AppID: f.app.ID,
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
	key string,
) executionstore.ChangeAgentConfigInput {
	return executionstore.ChangeAgentConfigInput{
		CreateAgentConfigInput: f.definition(t, instruction),
		AgentID:                agentID,
		ActorType:              identitystore.PrincipalTypeUser,
		ActorID:                f.user.ID,
		Reason:                 "app-capabilities-test",
		IdempotencyKey:         key,
	}
}

func (f appActivationFixture) subscriptions(t *testing.T, agentID uuid.UUID) []dbsqlc.AppSubscription {
	t.Helper()
	rows, err := f.store.pool.Query(f.ctx, `SELECT id,project_id,agent_id,app_id,subscription_type,
		scope_kind,scope_ref,events,created_at FROM app_subscriptions
		WHERE project_id=$1 AND agent_id=$2 ORDER BY id`, testProjectID, agentID)
	require.NoError(t, err)
	subscriptions, err := pgx.CollectRows(rows, pgx.RowToStructByName[dbsqlc.AppSubscription])
	require.NoError(t, err)
	return subscriptions
}

func (f appActivationFixture) attach(
	t *testing.T, agentID uuid.UUID, attachment integrationstore.AppSubscriptionAttachment,
) integrationstore.AppSubscriptionRecord {
	t.Helper()
	subscription, err := f.store.Integrations().CreateAppSubscription(f.ctx, integrationstore.CreateAppSubscriptionInput{
		OrgID: testOrgID, ProjectID: testProjectID, AgentID: agentID, AppID: attachment.AppID,
		Type: attachment.Type, Conversation: attachment.Conversation, Events: attachment.Events,
	})
	require.NoError(t, err)
	return subscription
}

func (f appActivationFixture) detach(t *testing.T, agentID uuid.UUID) {
	t.Helper()
	for _, subscription := range f.subscriptions(t, agentID) {
		require.NoError(t, f.store.Integrations().DeleteAppSubscription(
			f.ctx, testOrgID, testProjectID, subscription.AppID, subscription.ID))
	}
}

func (f appActivationFixture) disable(t *testing.T) {
	t.Helper()
	changed, err := f.store.Integrations().
		DisconnectProjectApp(f.ctx, integrationstore.DisconnectProjectAppInput{ProjectID: testProjectID, AppID: f.app.ID})
	require.NoError(t, err)
	require.True(t, changed)
}

func TestAppSubscriptionsDetachAndReattachIndependentOfConfig(t *testing.T) {
	f := newAppActivationFixture(t)
	definition := f.withSendingTools(t, f.definition(t, "Listen and send"))
	input := f.launchInput(uuid.Nil, "subscription-launch")
	input.DerivedConfig = &definition
	input.Subscriptions = []integrationstore.AppSubscriptionAttachment{f.attachment()}
	launch, err := f.store.Execution().LaunchAgent(f.ctx, input)
	require.NoError(t, err)
	original := f.subscriptions(t, launch.Agent.ID)[0]
	f.detach(t, launch.Agent.ID)
	_, err = f.store.Execution().ChangeAgentConfig(
		f.ctx,
		f.changeInput(t, launch.Agent.ID, "Config without app tools", "remove-tools"),
	)
	require.NoError(t, err)
	update := f.changeInput(t, launch.Agent.ID, "Tools restored", "restore-tools")
	update.CreateAgentConfigInput = f.withSendingTools(t, update.CreateAgentConfigInput)
	_, err = f.store.Execution().ChangeAgentConfig(f.ctx, update)
	require.NoError(t, err)
	require.Empty(t, f.subscriptions(t, launch.Agent.ID), "config changes cannot restore deleted routes")
	replayed, err := f.store.Execution().LaunchAgent(f.ctx, input)
	require.NoError(t, err)
	require.False(t, replayed.Created)
	require.Empty(t, f.subscriptions(t, launch.Agent.ID), "launch replay applies no attachments")
	replacement := f.attach(t, launch.Agent.ID, f.attachment())
	require.NotEqual(t, original.ID, replacement.ID)
	require.NoError(
		t,
		f.store.Integrations().DeleteAppSubscription(f.ctx, testOrgID, testProjectID, f.app.ID, original.ID),
	)
	after := f.subscriptions(t, launch.Agent.ID)
	require.Len(t, after, 1)
	require.Equal(t, replacement.ID, after[0].ID, "an old DELETE cannot detach the new attachment")
}

func TestAppSubscriptionsProfileLaunchActivationAndReplay(t *testing.T) {
	t.Parallel()
	f := newAppActivationFixture(t)
	config, err := f.store.Execution().CreateAgentConfig(f.ctx, f.definition(t, "Saved app config"))
	require.NoError(t, err)
	profile, err := f.store.Execution().CreateAgentProfile(f.ctx, executionstore.CreateAgentProfileInput{
		ProjectID: testProjectID, Name: "App profile", CurrentConfigID: config.ID,
	})
	require.NoError(t, err)
	var count int
	require.NoError(
		t,
		f.store.pool.QueryRow(
			f.ctx,
			`SELECT count(*) FROM app_subscriptions WHERE project_id=$1`,
			testProjectID,
		).Scan(&count),
	)
	require.Zero(t, count, "config/profile save must not subscribe")
	input := f.launchInput(config.ID, "app-launch")
	input.ProfileID = profile.ID
	input.Subscriptions = []integrationstore.AppSubscriptionAttachment{f.attachment()}
	input.Message = "Start with the attachment already present"
	launch, err := f.store.Execution().LaunchAgent(f.ctx, input)
	require.NoError(t, err)
	require.True(t, launch.Created)
	require.Equal(t, config.ID, launch.Agent.CurrentConfigID, "subscription-only launch reuses its config")
	require.NotEqual(t, uuid.Nil, launch.AgentInput.ID)
	before := f.subscriptions(t, launch.Agent.ID)
	require.Len(t, before, 1)
	require.Equal(t, "channel", before[0].ScopeKind)
	require.Equal(t, "C123", before[0].ScopeRef)
	require.Equal(t, []string{"message"}, before[0].Events)
	update := f.changeInput(t, launch.Agent.ID, "Unrelated instruction change", "app-edit")
	update.ExpectedCurrentConfigID = config.ID
	changed, err := f.store.Execution().ChangeAgentConfig(f.ctx, update)
	require.NoError(t, err)
	require.Equal(t, before, f.subscriptions(t, launch.Agent.ID))
	f.disable(t)
	removed, err := f.store.Execution().ChangeAgentConfig(
		f.ctx,
		f.changeInput(t, launch.Agent.ID, "Another config", "app-remove"),
	)
	require.NoError(t, err)
	require.Equal(t, before, f.subscriptions(t, launch.Agent.ID), "disconnection and config activation preserve routes")
	f.detach(t, launch.Agent.ID)
	old, err := f.store.Execution().ChangeAgentConfig(f.ctx, update)
	require.NoError(t, err)
	require.Equal(t, changed.ConfigChange.AgentInput.ID, old.ConfigChange.AgentInput.ID)
	require.Equal(t, changed.ConfigChange.Event.ID, old.ConfigChange.Event.ID)
	replayed, err := f.store.Execution().LaunchAgent(f.ctx, input)
	require.NoError(t, err)
	require.False(t, replayed.Created)
	require.Equal(t, removed.AgentConfig.ID, replayed.Agent.CurrentConfigID)
	require.Empty(t, f.subscriptions(t, launch.Agent.ID), "old replays cannot restore subscriptions")
	conflict := update
	conflict.Reason = "different intent"
	_, err = f.store.Execution().ChangeAgentConfig(f.ctx, conflict)
	require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
}

func TestAppSubscriptionsRejectCrossProjectApps(t *testing.T) {
	f := newAppActivationFixture(t)
	otherProject := seedAdditionalProjectForTest(t, f.ctx, f.store.pool, "other-app")
	credential := createIntegrationCredential(t, f.ctx, f.store, otherProject, f.user.ID, "other-app")
	otherInput := slackProjectAppSetupInput(f.user.ID, credential, "A_OTHER", "T_OTHER")
	otherInput.ProjectID = otherProject
	other := mustCreateProjectApp(t, f.ctx, f.store, otherInput)
	base, err := f.store.Execution().LaunchAgent(f.ctx, f.launchInput(f.profile.CurrentConfigID, "app-base"))
	require.NoError(t, err)
	for _, state := range []string{"active", "disconnected", "deleted", "missing"} {
		t.Run(state, func(t *testing.T) {
			if state == "disconnected" {
				_, err := f.store.Integrations().DisconnectProjectApp(
					f.ctx,
					integrationstore.DisconnectProjectAppInput{ProjectID: otherProject, AppID: other.ID},
				)
				require.NoError(t, err)
			}
			if state == "deleted" {
				require.NoError(t, f.store.Integrations().DeleteProjectApp(f.ctx, testOrgID, otherProject, other.ID))
			}
			attachment := f.attachment()
			attachment.AppID = other.ID
			if state == "missing" {
				attachment.AppID = uuid.New()
			}
			input := f.launchInput(f.profile.CurrentConfigID, state)
			input.Subscriptions = []integrationstore.AppSubscriptionAttachment{f.attachment(), attachment}
			_, err := f.store.Execution().LaunchAgent(f.ctx, input)
			require.ErrorIs(t, err, storeerr.ErrNotFound)
			_, err = f.store.Integrations().CreateAppSubscription(f.ctx, integrationstore.CreateAppSubscriptionInput{
				OrgID: testOrgID, ProjectID: testProjectID, AppID: attachment.AppID, AgentID: base.Agent.ID,
				Type: attachment.Type, Conversation: attachment.Conversation,
			})
			require.ErrorIs(t, err, storeerr.ErrNotFound)
			require.Empty(t, f.subscriptions(t, base.Agent.ID))
			change := f.changeInput(t, base.Agent.ID, "Foreign tool", "foreign-tool-"+state)
			definition := f.withSendingTools(t, change.CreateAgentConfigInput)
			var compiled agentconfig.Compiled
			require.NoError(t, json.Unmarshal(definition.CompiledDefinition, &compiled))
			for _, name := range []string{"app__chat__post_message", "app__chat__read"} {
				tool := compiled.Tools[name]
				tool.AppID = attachment.AppID
				compiled.Tools[name] = tool
			}
			change.CreateAgentConfigInput = f.encodedDefinition(t, compiled)
			_, err = f.store.Execution().ChangeAgentConfig(f.ctx, change)
			require.ErrorIs(t, err, storeerr.ErrNotFound)
			current, err := f.store.Execution().GetAgentInProject(f.ctx, testProjectID, base.Agent.ID)
			require.NoError(t, err)
			require.Equal(t, base.Agent.CurrentConfigID, current.CurrentConfigID)
			var count int
			require.NoError(
				t,
				f.store.pool.QueryRow(
					f.ctx,
					`SELECT count(*) FROM agents WHERE project_id=$1 AND idempotency_key=$2`,
					testProjectID,
					state,
				).Scan(&count),
			)
			require.Zero(t, count)
		})
	}
}

func TestAppConfigChangesRemainAvailableAtSubscriptionQuota(t *testing.T) {
	t.Parallel()
	f := newAppActivationFixture(t)
	base, err := f.store.Execution().LaunchAgent(f.ctx, f.launchInput(f.profile.CurrentConfigID, "quota-base"))
	require.NoError(t, err)
	_, err = f.store.pool.Exec(
		f.ctx,
		`INSERT INTO org_resource_limit_overrides(org_id,max_active_app_subscriptions_per_agent) VALUES($1,0)`,
		testOrgID,
	)
	require.NoError(t, err)
	_, err = f.store.Integrations().CreateAppSubscription(f.ctx, integrationstore.CreateAppSubscriptionInput{
		OrgID: testOrgID, ProjectID: testProjectID, AgentID: base.Agent.ID, AppID: f.app.ID,
		Type: f.attachment().Type, Conversation: f.attachment().Conversation,
	})
	require.ErrorIs(t, err, storeerr.ErrConflict)
	changed, err := f.store.Execution().ChangeAgentConfig(
		f.ctx,
		f.changeInput(t, base.Agent.ID, "Config is independent of quota", "quota-change"),
	)
	require.NoError(t, err)
	require.NotEqual(t, base.Agent.CurrentConfigID, changed.AgentConfig.ID)
	require.Empty(t, f.subscriptions(t, base.Agent.ID))
}

func TestAppSubscriptionQuotaPreservesExistingRoutesAndReleasesOnDetach(t *testing.T) {
	t.Parallel()
	f := newAppActivationFixture(t)
	input := f.launchInput(f.profile.CurrentConfigID, "existing-subscription")
	input.Subscriptions = []integrationstore.AppSubscriptionAttachment{f.attachment()}
	launch, err := f.store.Execution().LaunchAgent(f.ctx, input)
	require.NoError(t, err)
	before := f.subscriptions(t, launch.Agent.ID)
	_, err = f.store.pool.Exec(
		f.ctx,
		`INSERT INTO org_resource_limit_overrides(org_id,max_active_app_subscriptions_per_agent) VALUES($1,1)`,
		testOrgID,
	)
	require.NoError(t, err)
	duplicate := f.attach(t, launch.Agent.ID, f.attachment())
	require.Equal(t, before[0].ID, duplicate.ID)
	second := f.attachment()
	second.Conversation = json.RawMessage(`{"channel_id":"C456"}`)
	_, err = f.store.Integrations().CreateAppSubscription(f.ctx, integrationstore.CreateAppSubscriptionInput{
		OrgID: testOrgID, ProjectID: testProjectID, AgentID: launch.Agent.ID, AppID: second.AppID,
		Type: second.Type, Conversation: second.Conversation,
	})
	require.ErrorIs(t, err, storeerr.ErrConflict)
	require.Equal(t, before, f.subscriptions(t, launch.Agent.ID))
	_, err = f.store.Execution().ChangeAgentConfig(f.ctx, f.changeInput(t, launch.Agent.ID, "At quota", "at-quota"))
	require.NoError(t, err)
	require.Equal(t, before, f.subscriptions(t, launch.Agent.ID))
	f.detach(t, launch.Agent.ID)
	created := f.attach(t, launch.Agent.ID, second)
	require.Equal(t, "C456", created.Address.Ref)
	require.Len(t, f.subscriptions(t, launch.Agent.ID), 1)
}

func TestAppFailedConfigActivationPreservesSubscriptions(t *testing.T) {
	t.Parallel()
	f := newAppActivationFixture(t)
	input := f.launchInput(f.profile.CurrentConfigID, "config-conflict")
	input.Subscriptions = []integrationstore.AppSubscriptionAttachment{f.attachment()}
	launch, err := f.store.Execution().LaunchAgent(f.ctx, input)
	require.NoError(t, err)
	before := f.subscriptions(t, launch.Agent.ID)
	change := f.changeInput(t, launch.Agent.ID, "Stale config change", "failed-config-edit")
	change.ExpectedCurrentConfigID = uuid.New()
	_, err = f.store.Execution().ChangeAgentConfig(f.ctx, change)
	require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
	require.Equal(t, before, f.subscriptions(t, launch.Agent.ID))
	current, err := f.store.Execution().GetAgentInProject(f.ctx, testProjectID, launch.Agent.ID)
	require.NoError(t, err)
	require.Equal(t, launch.Agent.CurrentConfigID, current.CurrentConfigID)
	var count int
	require.NoError(t, f.store.pool.QueryRow(f.ctx,
		`SELECT count(*) FROM agent_inputs WHERE project_id=$1 AND agent_id=$2 AND input_idempotency_key=$3`,
		testProjectID, launch.Agent.ID, change.IdempotencyKey).Scan(&count))
	require.Zero(t, count, "failed activation must not retain its input")
}

func TestAppCapabilitiesLockAppsBeforeLaunchKeyAndProfile(t *testing.T) {
	t.Parallel()
	f := newAppActivationFixture(t)
	definition := f.definition(t, "Lock order launch")
	input := f.launchInput(uuid.Nil, "lock-order-launch")
	input.DerivedConfig, input.ProfileID = &definition, f.profile.ID
	input.Subscriptions = []integrationstore.AppSubscriptionAttachment{f.attachment()}
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
	require.Len(t, f.subscriptions(t, launch.Agent.ID), 1)
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
		"lock-order-change",
	)
	input.CreateAgentConfigInput = f.withSendingTools(t, input.CreateAgentConfigInput)
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
	require.NotEqual(t, launch.Agent.CurrentConfigID, changed.AgentConfig.ID)
	require.Empty(t, f.subscriptions(t, launch.Agent.ID), "activating tools must not attach subscriptions")
}

func TestAppCapabilitiesValidateCurrentConfigAfterAppWait(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"unconditional", "expected-current"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f := newAppActivationFixture(t)
			launch, err := f.store.Execution().LaunchAgent(f.ctx, f.launchInput(f.profile.CurrentConfigID, "concurrent-base"))
			require.NoError(t, err)
			input := f.changeInput(t, launch.Agent.ID, "Waiting change", "waiting-change")
			input.CreateAgentConfigInput = f.withSendingTools(t, input.CreateAgentConfigInput)
			if mode == "expected-current" {
				input.ExpectedCurrentConfigID = launch.Agent.CurrentConfigID
			}
			control := integrationdb.BeginTx(t, f.ctx, f.store.pool)
			require.NoError(t, dbsqlc.New(control).LockProjectAppLifecycleExclusive(
				f.ctx, dbsqlc.LockProjectAppLifecycleExclusiveParams{AppID: f.app.ID},
			))
			done := integrationdb.RunAsync(func() (executionstore.ChangeAgentConfigResult, error) {
				return f.store.Execution().IntegrationChangeAgentConfigOnce(f.ctx, input)
			})
			integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.store.pool, "LockProjectAppLifecycleShared", 1)
			concurrent, err := f.store.Execution().ChangeAgentConfig(
				f.ctx, f.changeInput(t, launch.Agent.ID, "Concurrent edit", "concurrent-edit"),
			)
			require.NoError(t, err)
			require.NoError(t, control.Commit(f.ctx))
			outcome := integrationdb.Await(t, done, "config change after concurrent edit")
			wantConfigID, wantInputs := concurrent.AgentConfig.ID, 0
			if mode == "expected-current" {
				require.ErrorIs(t, outcome.Err, storeerr.ErrStateTransitionConflict)
				var configs int
				require.NoError(t, f.store.pool.QueryRow(f.ctx,
					`SELECT count(*) FROM agent_configs WHERE project_id=$1 AND effective_definition_hash=$2`,
					testProjectID, input.EffectiveDefinitionHash).Scan(&configs))
				require.Zero(t, configs, "stale editor must roll back its config")
			} else {
				require.NoError(t, outcome.Err, "immutable next references need no transaction restart")
				wantConfigID, wantInputs = outcome.Value.AgentConfig.ID, 1
				require.Greater(t, outcome.Value.ConfigChange.Event.Sequence, concurrent.ConfigChange.Event.Sequence)
			}
			current, err := f.store.Execution().GetAgentInProject(f.ctx, testProjectID, launch.Agent.ID)
			require.NoError(t, err)
			require.Equal(t, wantConfigID, current.CurrentConfigID)
			var inputs, events int
			require.NoError(t, f.store.pool.QueryRow(f.ctx, `SELECT count(*), count(event.id)
				FROM agent_inputs input
				LEFT JOIN agent_events event ON event.agent_id=input.agent_id AND event.agent_input_id=input.id
				WHERE input.project_id=$1 AND input.agent_id=$2 AND input.input_idempotency_key=$3`,
				testProjectID, launch.Agent.ID, input.IdempotencyKey).Scan(&inputs, &events))
			require.Equal(t, wantInputs, inputs)
			require.Equal(t, wantInputs, events)
			require.Empty(t, f.subscriptions(t, launch.Agent.ID), "activating tools must not attach subscriptions")
		})
	}
}

func TestAppCapabilitiesUnavailableSecondaryDoesNotBlockLaunchOrConfigChange(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"disconnected", "deleted"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			f := newAppActivationFixture(t)
			secondary := (appInteractionFixture{ctx: f.ctx, store: f.store, user: f.user}).createApp(t, "secondary")
			definition := f.definition(t, "Saved before app revocation")
			var compiled agentconfig.Compiled
			require.NoError(t, json.Unmarshal(definition.CompiledDefinition, &compiled))
			compiled.Tools = map[string]agentconfig.ToolCompiled{
				"app__secondary__post_message": {
					Enabled: false, AppID: secondary.ID,
					Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow),
				},
			}
			config, err := f.store.Execution().CreateAgentConfig(f.ctx, f.encodedDefinition(t, compiled))
			require.NoError(t, err)
			attachment := f.attachment()
			attachment.AppID = secondary.ID
			baseInput := f.launchInput(f.profile.CurrentConfigID, "base")
			baseInput.Subscriptions = []integrationstore.AppSubscriptionAttachment{f.attachment(), attachment}
			base, err := f.store.Execution().LaunchAgent(f.ctx, baseInput)
			require.NoError(t, err)
			if state == "deleted" {
				require.NoError(t, f.store.Integrations().DeleteProjectApp(f.ctx, testOrgID, testProjectID, secondary.ID))
			} else {
				_, err := f.store.Integrations().DisconnectProjectApp(
					f.ctx,
					integrationstore.DisconnectProjectAppInput{ProjectID: testProjectID, AppID: secondary.ID},
				)
				require.NoError(t, err)
			}
			before := f.subscriptions(t, base.Agent.ID)
			launchInput := f.launchInput(config.ID, "after-revocation")
			launchInput.Subscriptions = []integrationstore.AppSubscriptionAttachment{f.attachment()}
			launch, err := f.store.Execution().LaunchAgent(f.ctx, launchInput)
			require.NoError(t, err)
			require.Len(t, f.subscriptions(t, launch.Agent.ID), 1)
			compiled.Instruction = "Unrelated instruction edit after revocation"
			change := f.changeInput(t, base.Agent.ID, compiled.Instruction, "edit-after-revocation")
			change.CreateAgentConfigInput = f.encodedDefinition(t, compiled)
			_, err = f.store.Execution().ChangeAgentConfig(f.ctx, change)
			require.NoError(t, err)
			require.Equal(t, before, f.subscriptions(t, base.Agent.ID))
			wantCount := 1
			if state == "disconnected" {
				wantCount = 2
			}
			require.Len(t, before, wantCount)
			matching := func(appID uuid.UUID) []dbsqlc.AppSubscription {
				t.Helper()
				rows, err := f.store.q.ListMatchingAppSubscriptions(f.ctx, dbsqlc.ListMatchingAppSubscriptionsParams{
					ProjectID: testProjectID, AppID: appID, Event: "message", Scopes: json.RawMessage(`[{"kind":"channel","ref":"C123"}]`),
				})
				require.NoError(t, err)
				return rows
			}
			require.Len(t, matching(f.app.ID), 2)
			require.Empty(t, matching(secondary.ID), "unavailable app cannot deliver")
			launchInput.IdempotencyKey = "required-secondary"
			launchInput.Subscriptions = append(launchInput.Subscriptions, attachment)
			_, err = f.store.Execution().LaunchAgent(f.ctx, launchInput)
			wantErr := storeerr.ErrUnauthorized
			if state == "deleted" {
				wantErr = storeerr.ErrNotFound
			}
			require.ErrorIs(t, err, wantErr)
			_, err = f.store.Integrations().CreateAppSubscription(f.ctx, integrationstore.CreateAppSubscriptionInput{
				OrgID: testOrgID, ProjectID: testProjectID, AgentID: base.Agent.ID, AppID: secondary.ID,
				Type: attachment.Type, Conversation: attachment.Conversation,
			})
			require.ErrorIs(t, err, wantErr)
			if state == "disconnected" {
				current, err := f.store.Integrations().GetProjectApp(f.ctx, testProjectID, secondary.ID)
				require.NoError(t, err)
				credential, err := f.store.Secrets().GetSecret(f.ctx, testOrgID, current.CredentialSecretID)
				require.NoError(t, err)
				_, err = f.store.Integrations().ConfigureProjectApp(f.ctx, integrationstore.ConfigureProjectAppInput{
					OrgID: testOrgID, ProjectID: testProjectID, AppID: current.ID, InstalledByUserID: f.user.ID,
					Provider: current.Provider, ProviderTenantID: current.ProviderTenantID,
					ProviderAccountRef: current.ProviderAccountRef,
					CredentialSecretID: current.CredentialSecretID, CredentialVersionID: credential.CurrentVersionID,
					ExpectedSetupRevision: current.SetupRevision, OAuthFlowID: uuid.Must(uuid.NewV7()),
					ProviderIdentity: current.ProviderIdentity,
				})
				require.NoError(t, err)
				require.Len(t, matching(secondary.ID), 1, "reconnect resumes the existing attachment, without config inheritance")
			}
		})
	}
}

func TestAppCapabilitiesInboxLaunchToleratesUnavailableSecondary(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"disconnected", "deleted"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			f := newInboxLaunchFixture(t, false, time.Minute, "a")
			secondary := (appInteractionFixture{ctx: f.ctx, store: f.store, user: f.user}).createApp(t, "secondary")
			definition := f.definition(t, "Secondary app is optional")
			var compiled agentconfig.Compiled
			require.NoError(t, json.Unmarshal(definition.CompiledDefinition, &compiled))
			compiled.Tools = map[string]agentconfig.ToolCompiled{"app__secondary__read": {
				Enabled: false, AppID: secondary.ID,
				Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow),
			}}
			definition = f.encodedDefinition(t, compiled)
			slot := f.slots["a"]
			slot.AgentID = uuid.Must(uuid.NewV7())
			slot.Selection.Address.Ref = "C123:789.012"
			slot.Launch.InitialInput.Origin.Address = slot.Selection.Address
			slot.Launch.InitialInput.SemanticEventKey = "message:789.012"
			slot.Launch.IdempotencyKey = "unavailable-secondary"
			saved, err := f.store.Execution().CreateAgentConfig(f.ctx, definition)
			require.NoError(t, err)
			slot.Launch.AgentConfigID = saved.ID
			attachment := f.attachment()
			attachment.Conversation, attachment.Events = json.RawMessage(`{"channel_id":"C123","thread_ts":"789.012"}`), []string{"message"}
			slot.Launch.Subscriptions = []integrationstore.AppSubscriptionAttachment{attachment}
			_, _, err = f.store.Integrations().AcceptIntegrationReceipt(f.ctx, integrationstore.VerifiedIntegrationReceipt{
				ProjectID: testProjectID, AppID: f.app.ID, ReceiptKey: "unavailable-secondary", Payload: []byte(`{}`),
			})
			require.NoError(t, err)
			receipt, found, err := f.store.Integrations().ClaimIntegrationInbox(
				f.ctx,
				integrationstore.ClaimIntegrationInboxInput{ProjectID: testProjectID, AppID: f.app.ID, LeaseDuration: time.Minute},
			)
			require.NoError(t, err)
			require.True(t, found)
			plan, err := json.Marshal(map[string]executionstore.InboxLaunchSlot{"a": slot})
			require.NoError(t, err)
			require.NoError(t, f.store.Integrations().WithIntegrationInboxLease(f.ctx, receipt.Lease(),
				func(work *integrationstore.IntegrationInboxLeaseTx) error { return work.FreezePlan(f.ctx, plan) }))
			if state == "deleted" {
				require.NoError(t, f.store.Integrations().DeleteProjectApp(f.ctx, testOrgID, testProjectID, secondary.ID))
			} else {
				_, err := f.store.Integrations().DisconnectProjectApp(
					f.ctx,
					integrationstore.DisconnectProjectAppInput{ProjectID: testProjectID, AppID: secondary.ID},
				)
				require.NoError(t, err)
			}
			launch, err := f.store.Execution().AdmitInboxLaunchSlot(f.ctx, receipt.Lease(), "a")
			require.NoError(t, err)
			require.True(t, launch.Created)
			require.Equal(t, slot.AgentID, launch.Agent.ID)
			subscriptions := f.subscriptions(t, launch.Agent.ID)
			require.Len(t, subscriptions, 1, "only the explicit primary attachment is created")
			require.Equal(t, slot.Selection.Address.Ref, subscriptions[0].ScopeRef)
			replay, err := f.store.Execution().AdmitInboxLaunchSlot(f.ctx, receipt.Lease(), "a")
			require.NoError(t, err)
			require.False(t, replay.Created)
		})
	}
}

func TestAppSubscriptionLaunchBatchQuotaRollbackAndDeduplication(t *testing.T) {
	t.Parallel()
	f := newAppActivationFixture(t)
	_, err := f.store.pool.Exec(
		f.ctx,
		`INSERT INTO org_resource_limit_overrides(org_id,max_active_app_subscriptions_per_agent) VALUES($1,1)`,
		testOrgID,
	)
	require.NoError(t, err)
	definition := f.definition(t, "Atomic attachment batch")
	input := f.launchInput(uuid.Nil, "subscription-batch")
	input.DerivedConfig = &definition
	input.Message = "Atomic initial input"
	second := f.attachment()
	second.Conversation = json.RawMessage(`{"channel_id":"C456"}`)
	input.Subscriptions = []integrationstore.AppSubscriptionAttachment{f.attachment(), second}
	_, err = f.store.Execution().LaunchAgent(f.ctx, input)
	require.ErrorIs(t, err, storeerr.ErrConflict)
	var agents, configs, subscriptions, inputs int
	require.NoError(t, f.store.pool.QueryRow(f.ctx, `SELECT
		(SELECT count(*) FROM agents WHERE project_id=$1 AND idempotency_key=$2),
		(SELECT count(*) FROM agent_configs WHERE project_id=$1 AND effective_definition_hash=$3),
		(SELECT count(*) FROM app_subscriptions WHERE project_id=$1),
		(SELECT count(*) FROM agent_inputs WHERE project_id=$1 AND input_kind='content')`,
		testProjectID, input.IdempotencyKey, definition.EffectiveDefinitionHash).Scan(
		&agents,
		&configs,
		&subscriptions,
		&inputs,
	))
	require.Zero(t, agents)
	require.Zero(t, configs)
	require.Zero(t, subscriptions, "quota failure rolls back every attachment in the batch")
	require.Zero(t, inputs)
	input.Subscriptions[1] = input.Subscriptions[0]
	launch, err := f.store.Execution().LaunchAgent(f.ctx, input)
	require.NoError(t, err, "duplicate matching attachments count once toward quota")
	require.True(t, launch.Created)
	require.Len(t, f.subscriptions(t, launch.Agent.ID), 1)
}

func TestAppSubscriptionConcurrentAttachmentsSerializeQuota(t *testing.T) {
	t.Parallel()
	f := newAppActivationFixture(t)
	launch, err := f.store.Execution().LaunchAgent(f.ctx, f.launchInput(f.profile.CurrentConfigID, "concurrent-quota"))
	require.NoError(t, err)
	_, err = f.store.pool.Exec(
		f.ctx,
		`INSERT INTO org_resource_limit_overrides(org_id,max_active_app_subscriptions_per_agent) VALUES($1,1)`,
		testOrgID,
	)
	require.NoError(t, err)
	control := integrationdb.BeginTx(t, f.ctx, f.store.pool)
	_, err = dbsqlc.New(control).LockAgentInProject(
		f.ctx,
		dbsqlc.LockAgentInProjectParams{ProjectID: testProjectID, ID: launch.Agent.ID},
	)
	require.NoError(t, err)
	var pending []<-chan integrationdb.AsyncResult[integrationstore.AppSubscriptionRecord]
	for _, conversation := range []string{`{"channel_id":"C123"}`, `{"channel_id":"C456"}`} {
		pending = append(pending, integrationdb.RunAsync(func() (integrationstore.AppSubscriptionRecord, error) {
			return f.store.Integrations().CreateAppSubscription(f.ctx, integrationstore.CreateAppSubscriptionInput{
				OrgID: testOrgID, ProjectID: testProjectID, AgentID: launch.Agent.ID, AppID: f.app.ID,
				Type: "thread_messages", Conversation: json.RawMessage(conversation),
			})
		}))
	}
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.store.pool, "LockAgentInProject", 2)
	require.NoError(t, control.Commit(f.ctx))
	var succeeded, rejected int
	for _, done := range pending {
		outcome := integrationdb.Await(t, done, "concurrent subscription attachment")
		if outcome.Err == nil {
			succeeded++
		} else {
			require.ErrorIs(t, outcome.Err, storeerr.ErrConflict)
			rejected++
		}
	}
	require.Equal(t, 1, succeeded)
	require.Equal(t, 1, rejected)
	require.Len(t, f.subscriptions(t, launch.Agent.ID), 1)
}
