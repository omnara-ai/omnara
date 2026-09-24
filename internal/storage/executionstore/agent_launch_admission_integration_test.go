//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/patch"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestLaunchInitialContentOriginAndReplay(t *testing.T) {
	t.Parallel()
	f := newIntegrationActivationFixture(t)
	definition := f.definition(t, "Initial origin")
	input := f.launchInput(uuid.Nil, "initial-origin")
	input.DerivedConfig = &definition
	input.Subscriptions = []integrationstore.IntegrationSubscriptionAttachment{f.attachment()}
	input.InitialInput = &executionstore.LaunchInitialInput{
		ContentBlocks: json.RawMessage(
			`[{"type":"text","text":"First","metadata":{"part":"1"}},{"type":"text","text":"Second"}]`,
		),
		Metadata: json.RawMessage(`{"event":"message","timestamp":"123.456"}`),
		Actor:    mustIntegrationActorParams(t, f.integration.ID, "U_INITIAL"),
		Origin: &executionstore.LaunchInputOrigin{
			IntegrationID: f.integration.ID,
			Address:       integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:123.456"},
			DisplayName:   "Initial thread",
		},
		DeliveryMode:           executionstore.DeliveryModeSteering,
		CancelOpenInteractions: true,
		SemanticEventKey:       "message:123.456",
	}
	launch, err := f.store.Execution().LaunchAgent(f.ctx, input)
	require.NoError(t, err)
	require.True(t, launch.Created)
	require.NotEqual(t, uuid.Nil, launch.AgentInput.ID)
	require.NotEqual(t, uuid.Nil, launch.AgentInput.ActorID)
	require.Equal(t, executionstore.DeliveryModeSteering, launch.AgentInput.DeliveryMode)
	require.JSONEq(t, string(input.InitialInput.Metadata), string(launch.AgentInput.Metadata))
	require.JSONEq(t, string(input.InitialInput.ContentBlocks), string(launch.InputContentBlocks))
	require.Equal(t, launch.IntegrationTarget.ID, launch.AgentInput.IntegrationTargetID)
	require.Empty(t, launch.IntegrationTarget.SelectionSlot)
	_, assigned, err := f.store.Integrations().GetAgentIntegrationConversation(
		f.ctx,
		testProjectID,
		launch.Agent.ID,
		f.integration.ID,
	)
	require.NoError(t, err)
	require.False(t, assigned, "an input origin and subscription do not assign a tool conversation")
	require.Equal(t, "integration:slack:"+f.integration.ID.String(), launch.AgentInput.IdempotencyScope)
	require.Equal(t, input.InitialInput.SemanticEventKey, launch.AgentInput.InputIdempotencyKey)
	require.Len(t, f.subscriptions(t, launch.Agent.ID), 1)
	f.disable(t)
	replayed, err := f.store.Execution().LaunchAgent(f.ctx, input)
	require.NoError(t, err)
	require.False(t, replayed.Created)
	require.Equal(t, launch.Agent.ID, replayed.Agent.ID)
	var count int
	require.NoError(
		t,
		f.store.pool.QueryRow(
			f.ctx,
			`SELECT count(*) FROM agent_inputs WHERE agent_id=$1 AND input_kind='content'`,
			launch.Agent.ID,
		).
			Scan(
				&count,
			),
	)
	require.Equal(t, 1, count)
}

func TestLaunchInitialInputFailureRollsBackAgentConfigAndTarget(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"actor-tenant", "missing-artifact", "queued-cancellation"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			f := newIntegrationActivationFixture(t)
			definition := f.definition(t, "Failed initial input")
			input := f.launchInput(uuid.Nil, "invalid-initial")
			input.DerivedConfig = &definition
			input.Subscriptions = []integrationstore.IntegrationSubscriptionAttachment{f.attachment()}
			input.InitialInput = &executionstore.LaunchInitialInput{
				ContentBlocks: json.RawMessage(`[{"type":"text","text":"test"}]`),
				Actor:         mustIntegrationActorParams(t, f.integration.ID, "U_INITIAL"),
				Origin: &executionstore.LaunchInputOrigin{
					IntegrationID: f.integration.ID,
					Address:       integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:123.456"},
				},
				SemanticEventKey: "message:123.456",
			}
			switch scenario {
			case "actor-tenant":
				input.InitialInput.Actor.ProviderTenantID = "T_WRONG"
			case "missing-artifact":
				input.InitialInput.ContentBlocks = json.RawMessage(
					`[{"type":"media_ref","artifact_id":"` + uuid.NewString() + `"}]`,
				)
			case "queued-cancellation":
				input.InitialInput.CancelOpenInteractions = true
			}
			_, err := f.store.Execution().LaunchAgent(f.ctx, input)
			require.Error(t, err)
			for _, check := range []struct{ query, value string }{
				{`SELECT count(*) FROM agents WHERE idempotency_key=$1`, input.IdempotencyKey},
				{`SELECT count(*) FROM agent_configs WHERE effective_definition_hash=$1`, definition.EffectiveDefinitionHash},
				{`SELECT count(*) FROM integration_targets WHERE provider_ref=$1`, input.InitialInput.Origin.Address.Ref},
				{`SELECT count(*) FROM integration_subscriptions WHERE integration_id=$1`, f.integration.ID.String()},
			} {
				var count int
				require.NoError(t, f.store.pool.QueryRow(f.ctx, check.query, check.value).Scan(&count))
				require.Zero(t, count)
			}
		})
	}
}

func TestLaunchPreparedArtifactMetadataReplayAndRollback(t *testing.T) {
	t.Parallel()
	f := newIntegrationActivationFixture(t)
	launch, err := f.store.Execution().LaunchAgent(f.ctx, f.launchInput(f.profile.CurrentConfigID, "prepared-metadata"))
	require.NoError(t, err)
	id, err := uuid.NewV7()
	require.NoError(t, err)
	file := artifactstore.PreparedArtifact{
		ID:          id,
		ContentType: "text/plain",
		Filename:    "note.txt",
		Digest:      blobstore.ContentDigest([]byte("hello")),
		SizeBytes:   5,
	}
	tx := integrationdb.BeginTx(t, f.ctx, f.store.pool)
	records, err := artifactstore.InsertPreparedArtifactsTx(
		f.ctx,
		tx,
		testProjectID,
		launch.Agent.ID,
		[]artifactstore.PreparedArtifact{file},
	)
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.True(t, records[0].Created)
	require.NoError(t, tx.Commit(f.ctx))
	tx = integrationdb.BeginTx(t, f.ctx, f.store.pool)
	records, err = artifactstore.InsertPreparedArtifactsTx(
		f.ctx,
		tx,
		testProjectID,
		launch.Agent.ID,
		[]artifactstore.PreparedArtifact{file},
	)
	require.NoError(t, err)
	require.False(t, records[0].Created)
	require.NoError(t, tx.Commit(f.ctx))
	tx = integrationdb.BeginTx(t, f.ctx, f.store.pool)
	other := file
	other.ID, err = uuid.NewV7()
	require.NoError(t, err)
	file.SizeBytes++
	_, err = artifactstore.InsertPreparedArtifactsTx(
		f.ctx,
		tx,
		testProjectID,
		launch.Agent.ID,
		[]artifactstore.PreparedArtifact{other, file},
	)
	require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
	require.NoError(t, tx.Rollback(f.ctx))
	_, err = f.store.Artifacts().GetArtifact(f.ctx, testProjectID, launch.Agent.ID, other.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
}

type inboxLaunchFixture struct {
	integrationActivationFixture
	receipt integrationstore.IntegrationInboxRecord
	slots   map[string]executionstore.InboxLaunchSlot
}

func newInboxLaunchFixture(
	t *testing.T,
	withFile bool,
	leaseDuration time.Duration,
	keys ...string,
) inboxLaunchFixture {
	t.Helper()
	return newInboxLaunchFixtureForIntegration(t, newIntegrationActivationFixture(t), withFile, leaseDuration, keys...)
}

func newInboxLaunchFixtureForIntegration(
	t *testing.T,
	integration integrationActivationFixture,
	withFile bool,
	leaseDuration time.Duration,
	keys ...string,
) inboxLaunchFixture {
	t.Helper()
	f := inboxLaunchFixture{
		integrationActivationFixture: integration,
		slots:                        map[string]executionstore.InboxLaunchSlot{},
	}
	setup := integrationstore.SaveProjectIntegrationInput{
		OrgID:           testOrgID,
		ProjectID:       testProjectID,
		Name:            f.integration.Name,
		IntegrationType: integrationdefinition.SlackThread,
		Settings: integrationstore.ProjectIntegrationSettings{
			Launcher: &integrationstore.IntegrationLauncher{Trigger: "mention", ScopeKind: "channel", ScopeRef: "C123"},
		},
	}
	for _, key := range keys {
		setup.Settings.Launcher.Slots = append(
			setup.Settings.Launcher.Slots,
			integrationstore.IntegrationLaunchSlot{Key: key, AgentProfileID: &f.profile.ID},
		)
	}
	var err error
	f.integration, err = f.store.Integrations().UpdateProjectIntegration(f.ctx, f.integration.ID, setup)
	require.NoError(t, err)
	_, _, err = f.store.Integrations().
		AcceptIntegrationReceipt(
			f.ctx,
			integrationstore.VerifiedIntegrationReceipt{
				ProjectID:     testProjectID,
				IntegrationID: f.integration.ID,
				ReceiptKey:    "launch-event",
				Payload:       []byte(`{"event":"message"}`),
			},
		)
	require.NoError(t, err)
	var found bool
	f.receipt, found, err = f.store.Integrations().
		ClaimIntegrationInbox(
			f.ctx,
			integrationstore.ClaimIntegrationInboxInput{
				ProjectID:     testProjectID,
				IntegrationID: f.integration.ID,
				LeaseDuration: leaseDuration,
			},
		)
	require.NoError(t, err)
	require.True(t, found)
	for _, key := range keys {
		plannedID, err := uuid.NewV7()
		require.NoError(t, err)
		definition := f.definition(t, "Frozen initial config "+key)
		launch := f.launchInput(uuid.Nil, "inbox-launch-"+key)
		launch.DerivedConfig, launch.ProfileID = &definition, f.profile.ID
		launch.DerivedBaseConfigID = f.profile.CurrentConfigID
		subscription := f.attachment()
		subscription.Conversation = json.RawMessage(`{"channel_id":"C123","thread_ts":"123.456"}`)
		launch.Subscriptions = []integrationstore.IntegrationSubscriptionAttachment{subscription}
		launch.InitialInput = &executionstore.LaunchInitialInput{
			ContentBlocks: json.RawMessage(
				`[{"type":"text","text":"First event"}]`,
			),
			Metadata: json.RawMessage(`{"event":"frozen-first"}`),
			Actor:    mustIntegrationActorParams(t, f.integration.ID, "U_LAUNCH"),
			Origin: &executionstore.LaunchInputOrigin{
				IntegrationID: f.integration.ID,
				Address:       integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:123.456"},
			},
			SemanticEventKey: "message:123.456",
			DeliveryMode:     executionstore.DeliveryModeSteering,
		}
		slot := executionstore.InboxLaunchSlot{
			AgentID: plannedID,
			Launch:  f.freezeLaunch(t, launch),
			Selection: integrationstore.InboxIntegrationSelection{
				IntegrationID: f.integration.ID,
				Address:       launch.InitialInput.Origin.Address,
				Slot:          key,
			},
		}
		if withFile {
			fileID, err := uuid.NewV7()
			require.NoError(t, err)
			slot.ArtifactIDs = []uuid.UUID{fileID}
			slot.Launch.InitialInput.ContentBlocks = json.RawMessage(
				`[{"type":"text","text":"First event"},{"type":"media_ref","artifact_id":"` + fileID.String() + `"}]`,
			)
		}
		f.slots[key] = slot
	}
	plan, err := json.Marshal(f.slots)
	require.NoError(t, err)
	require.NoError(
		t,
		f.store.Integrations().
			WithIntegrationInboxLease(
				f.ctx,
				f.receipt.Lease(),
				func(work *integrationstore.IntegrationInboxLeaseTx) error { return work.FreezePlan(f.ctx, plan) },
			),
	)
	return f
}

func (f inboxLaunchFixture) prepare(t *testing.T, key string) {
	t.Helper()
	var prepared executionstore.InboxLaunchPreparation
	for _, id := range f.slots[key].ArtifactIDs {
		prepared.Artifacts = append(
			prepared.Artifacts,
			artifactstore.PreparedArtifact{
				ID:          id,
				ContentType: "text/plain",
				Filename:    "first.txt",
				Digest:      blobstore.ContentDigest([]byte("first")),
				SizeBytes:   5,
			},
		)
	}
	raw, err := json.Marshal(prepared)
	require.NoError(t, err)
	require.NoError(
		t,
		f.store.Integrations().
			WithIntegrationInboxLease(
				f.ctx,
				f.receipt.Lease(),
				func(work *integrationstore.IntegrationInboxLeaseTx) error { return work.PrepareSlot(f.ctx, key, raw) },
			),
	)
}

func (f integrationActivationFixture) freezeLaunch(
	t *testing.T, launch executionstore.LaunchAgentInput,
) executionstore.InboxLaunchPlan {
	t.Helper()
	config, err := f.store.Execution().CreateAgentConfig(f.ctx, *launch.DerivedConfig)
	require.NoError(t, err)
	return executionstore.InboxLaunchPlan{
		ProfileID: launch.ProfileID, AgentConfigID: config.ID, DerivedBaseConfigID: launch.DerivedBaseConfigID,
		LaunchedBy:     executionstore.InboxLaunchPrincipal{Type: launch.LaunchedBy.Type, ID: launch.LaunchedBy.ID},
		IdempotencyKey: launch.IdempotencyKey, InitialInput: launch.InitialInput, Subscriptions: launch.Subscriptions,
	}
}

func (f inboxLaunchFixture) assertAbsent(t *testing.T, key string) {
	t.Helper()
	slot := f.slots[key]
	for _, check := range []struct {
		query string
		arg   any
	}{
		{`SELECT count(*) FROM agents WHERE id=$1`, slot.AgentID},
		{`SELECT count(*) FROM integration_targets WHERE agent_id=$1`, slot.AgentID},
		{`SELECT count(*) FROM integration_subscriptions WHERE agent_id=$1`, slot.AgentID},
		{`SELECT count(*) FROM integration_states WHERE kind='agent_conversation' AND key=$1`, slot.AgentID.String()},
		{`SELECT count(*) FROM agent_inputs WHERE agent_id=$1`, slot.AgentID},
		{`SELECT count(*) FROM artifacts WHERE agent_id=$1`, slot.AgentID},
	} {
		var count int
		require.NoError(t, f.store.pool.QueryRow(f.ctx, check.query, check.arg).Scan(&count))
		require.Zero(t, count, check.query)
	}
	receipt, err := f.store.Integrations().GetIntegrationInbox(f.ctx, testProjectID, f.receipt.ID)
	require.NoError(t, err)
	var progress map[string]map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(receipt.Progress, &progress))
	require.Empty(t, progress[key]["committed"])
}

func TestInboxLaunchFilesAtomicConcurrentAndReplay(t *testing.T) {
	t.Parallel()
	f := newInboxLaunchFixture(t, true, time.Minute, "a")
	_, err := f.store.Execution().AdmitInboxLaunchSlot(f.ctx, f.receipt.Lease(), "a")
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
	f.assertAbsent(t, "a")
	f.prepare(t, "a")
	start := make(chan struct{})
	admit := func() (executionstore.LaunchAgentResult, error) {
		<-start
		return f.store.Execution().AdmitInboxLaunchSlot(f.ctx, f.receipt.Lease(), "a")
	}
	first, second := integrationdb.RunAsync(admit), integrationdb.RunAsync(admit)
	close(start)
	results := []executionstore.LaunchAgentResult{
		integrationdb.AwaitSuccess(t, first, "first admission"),
		integrationdb.AwaitSuccess(t, second, "duplicate admission"),
	}
	require.NotEqual(t, results[0].Created, results[1].Created)
	var created executionstore.LaunchAgentResult
	for _, result := range results {
		require.Equal(t, f.slots["a"].AgentID, result.Agent.ID)
		if result.Created {
			created = result
		}
	}
	require.Equal(t, f.integration.ID, created.IntegrationTarget.IntegrationID)
	require.Equal(t, "a", created.IntegrationTarget.SelectionSlot)
	conversation, found, err := f.store.Integrations().GetAgentIntegrationConversation(
		f.ctx, testProjectID, created.Agent.ID, f.integration.ID,
	)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, f.slots["a"].Selection.Address, conversation)
	require.Equal(t, created.IntegrationTarget.ID, created.AgentInput.IntegrationTargetID)
	require.Len(t, created.Artifacts, 1)
	require.Equal(t, f.slots["a"].ArtifactIDs[0], created.Artifacts[0].ID)
	require.Equal(t, created.Agent.ID, created.Artifacts[0].AgentID)
	require.JSONEq(t, string(f.slots["a"].Launch.InitialInput.ContentBlocks), string(created.InputContentBlocks))
	subscriptions := f.subscriptions(t, created.Agent.ID)
	require.Len(t, subscriptions, 1, "one concrete frozen attachment is registered atomically")
	require.Equal(t, "thread", subscriptions[0].ScopeKind)
	require.Equal(t, "C123:123.456", subscriptions[0].ScopeRef)
	var count int
	require.NoError(
		t,
		f.store.pool.QueryRow(
			f.ctx,
			`SELECT count(*) FROM agent_inputs WHERE agent_id=$1 AND input_kind='content'`,
			created.Agent.ID,
		).
			Scan(
				&count,
			),
	)
	require.Equal(t, 1, count)
	require.NoError(
		t,
		f.store.Integrations().
			WithIntegrationInboxLease(
				f.ctx,
				f.receipt.Lease(),
				func(work *integrationstore.IntegrationInboxLeaseTx) error { return work.Complete(f.ctx) },
			),
	)
	f.disable(t)
	changed, err := f.store.Execution().
		ChangeAgentConfig(f.ctx, f.changeInput(t, created.Agent.ID, "After launch", "after-launch"))
	require.NoError(t, err)
	require.Equal(t, subscriptions, f.subscriptions(t, created.Agent.ID),
		"config activation and disconnection preserve subscriptions")
	for _, subscription := range subscriptions {
		require.NoError(t, f.store.Integrations().DeleteIntegrationSubscription(
			f.ctx, testOrgID, testProjectID, subscription.IntegrationID, subscription.ID))
	}
	replayed, err := f.store.Execution().AdmitInboxLaunchSlot(f.ctx, f.receipt.Lease(), "a")
	require.NoError(t, err)
	require.False(t, replayed.Created)
	require.Equal(t, changed.AgentConfig.ID, replayed.Agent.CurrentConfigID)
	require.Empty(t, f.subscriptions(t, created.Agent.ID), "replay cannot rematerialize old resources")
}

func TestInboxLaunchLeaseExpiryRollsBackAllAdmissionRows(t *testing.T) {
	t.Parallel()
	f := newInboxLaunchFixture(t, true, 2*time.Second, "a")
	f.prepare(t, "a")
	blocker := integrationdb.BeginTx(t, f.ctx, f.store.pool)
	require.NoError(
		t,
		dbsqlc.New(blocker).
			LockAgentLaunchIdempotencyKey(
				f.ctx,
				dbsqlc.LockAgentLaunchIdempotencyKeyParams{
					ProjectID:      testProjectID,
					IdempotencyKey: f.slots["a"].Launch.IdempotencyKey,
				},
			),
	)
	done := integrationdb.RunAsync(func() (executionstore.LaunchAgentResult, error) {
		return f.store.Execution().AdmitInboxLaunchSlot(f.ctx, f.receipt.Lease(), "a")
	})
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.store.pool, "LockAgentLaunchIdempotencyKey", 1)
	// The lease is issued and fenced by PostgreSQL's clock, which may differ
	// from the test host's clock. Wait there while retaining the launch blocker.
	waitCtx, cancelWait := context.WithTimeout(f.ctx, 3*time.Second)
	defer cancelWait()
	_, err := blocker.Exec(waitCtx,
		`SELECT pg_sleep(GREATEST(0, EXTRACT(EPOCH FROM ($1::timestamptz - clock_timestamp()))))`,
		*f.receipt.ClaimExpiresAt)
	require.NoError(t, err, "wait for PostgreSQL inbox lease expiry")
	require.NoError(t, blocker.Commit(f.ctx))
	result := integrationdb.Await(t, done, "expired admission")
	require.ErrorIs(t, result.Err, integrationstore.ErrIntegrationInboxLeaseLost)
	f.assertAbsent(t, "a")
	receipt, err := f.store.Integrations().GetIntegrationInbox(f.ctx, testProjectID, f.receipt.ID)
	require.NoError(t, err)
	var progress map[string]map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(receipt.Progress, &progress))
	require.NotEmpty(t, progress["a"]["prepared"])
	recovered, err := f.store.Integrations().RecoverIntegrationInbox(f.ctx, 10)
	require.NoError(t, err)
	require.EqualValues(t, 1, recovered)
	reclaimed, found, err := f.store.Integrations().
		ClaimIntegrationInbox(
			f.ctx,
			integrationstore.ClaimIntegrationInboxInput{
				ProjectID:     testProjectID,
				IntegrationID: f.integration.ID,
				LeaseDuration: time.Minute,
			},
		)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, receipt.ID, reclaimed.ID)
	require.NotEqual(t, receipt.ClaimToken, reclaimed.ClaimToken)
	require.JSONEq(t, string(receipt.Plan), string(reclaimed.Plan))
	require.JSONEq(t, string(receipt.Progress), string(reclaimed.Progress))
	admitted, err := f.store.Execution().AdmitInboxLaunchSlot(f.ctx, reclaimed.Lease(), "a")
	require.NoError(t, err)
	require.True(t, admitted.Created)
	require.Equal(t, f.slots["a"].AgentID, admitted.Agent.ID)
	require.Equal(
		t,
		f.slots["a"].Launch.AgentConfigID,
		admitted.AgentConfig.ID,
	)
	require.Len(t, admitted.Artifacts, 1)
	require.Equal(t, f.slots["a"].ArtifactIDs[0], admitted.Artifacts[0].ID)
}

func TestInboxLaunchPreparationCannotReplaceFrozenArtifactIdentity(t *testing.T) {
	t.Parallel()
	f := newInboxLaunchFixture(t, true, time.Minute, "a")
	replacement, err := uuid.NewV7()
	require.NoError(t, err)
	raw, err := json.Marshal(
		executionstore.InboxLaunchPreparation{
			Artifacts: []artifactstore.PreparedArtifact{
				{
					ID:          replacement,
					ContentType: "text/plain",
					Digest:      blobstore.ContentDigest([]byte("other")),
					SizeBytes:   5,
				},
			},
		},
	)
	require.NoError(t, err)
	require.NoError(
		t,
		f.store.Integrations().
			WithIntegrationInboxLease(
				f.ctx,
				f.receipt.Lease(),
				func(work *integrationstore.IntegrationInboxLeaseTx) error { return work.PrepareSlot(f.ctx, "a", raw) },
			),
	)
	_, err = f.store.Execution().AdmitInboxLaunchSlot(f.ctx, f.receipt.Lease(), "a")
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
	f.assertAbsent(t, "a")
}

func TestInboxLaunchRetainsFrozenMembershipAcrossIntegrationEdit(t *testing.T) {
	t.Parallel()
	f := newInboxLaunchFixture(t, true, time.Minute, "a", "b")
	f.prepare(t, "a")
	first, err := f.store.Execution().AdmitInboxLaunchSlot(f.ctx, f.receipt.Lease(), "a")
	require.NoError(t, err)
	_, err = f.store.Execution().AdmitInboxLaunchSlot(f.ctx, f.receipt.Lease(), "b")
	require.Error(t, err)
	f.assertAbsent(t, "b")
	settings := f.integration.Settings
	settings.Launcher.Slots[1].Key = "c"
	_, err = f.store.Integrations().
		UpdateProjectIntegration(
			f.ctx,
			f.integration.ID,
			integrationstore.SaveProjectIntegrationInput{
				OrgID:           testOrgID,
				ProjectID:       testProjectID,
				Name:            f.integration.Name,
				IntegrationType: f.integration.IntegrationType,
				Settings:        settings,
			},
		)
	require.NoError(t, err)
	f.prepare(t, "b")
	second, err := f.store.Execution().AdmitInboxLaunchSlot(f.ctx, f.receipt.Lease(), "b")
	require.NoError(t, err)
	require.Equal(t, "b", second.IntegrationTarget.SelectionSlot)
	require.Equal(t, f.slots["b"].AgentID, second.Agent.ID)
	require.Equal(
		t,
		f.slots["b"].Launch.AgentConfigID,
		second.AgentConfig.ID,
	)
	require.NotEqual(t, first.Agent.ID, second.Agent.ID)
	_, err = f.store.Execution().AdmitInboxLaunchSlot(f.ctx, f.receipt.Lease(), "c")
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
}

func TestInboxLaunchRejectsIdempotencyKeyForDifferentPlannedAgent(t *testing.T) {
	t.Parallel()
	f := newInboxLaunchFixture(t, true, time.Minute, "a")
	f.prepare(t, "a")
	other, err := f.store.Execution().
		LaunchAgent(f.ctx, f.launchInput(f.profile.CurrentConfigID, f.slots["a"].Launch.IdempotencyKey))
	require.NoError(t, err)
	require.NotEqual(t, f.slots["a"].AgentID, other.Agent.ID)
	_, err = f.store.Execution().AdmitInboxLaunchSlot(f.ctx, f.receipt.Lease(), "a")
	require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
	f.assertAbsent(t, "a")
}

func TestInboxLaunchLocksSecondaryIntegrationBeforeReceipt(t *testing.T) {
	t.Parallel()
	f := newInboxLaunchFixture(t, false, time.Minute, "a")
	credential := createIntegrationCredential(t, f.ctx, f.store, testProjectID, f.user.ID, "secondary-launch")
	secondary := mustCreateProjectIntegration(
		t,
		f.ctx,
		f.store,
		slackProjectIntegrationSetupInput(f.user.ID, credential, "A_SECONDARY", "T_SECONDARY"),
	)
	resource := f.attachment()
	resource.IntegrationID = secondary.ID
	resource.Conversation = json.RawMessage(`{"channel_id":"CSECOND","thread_ts":"456.789"}`)
	definition := f.definition(t, "Secondary subscription gate")
	slot := f.slots["a"]
	var err error
	slot.AgentID, err = uuid.NewV7()
	require.NoError(t, err)
	slot.Selection.Address.Ref = "C123:789.012"
	initial := *slot.Launch.InitialInput
	origin := *initial.Origin
	origin.Address = slot.Selection.Address
	initial.Origin, initial.SemanticEventKey = &origin, "message:789.012"
	slot.Launch.InitialInput = &initial
	saved, err := f.store.Execution().CreateAgentConfig(f.ctx, definition)
	require.NoError(t, err)
	slot.Launch.AgentConfigID = saved.ID
	primary := f.attachment()
	primary.Conversation = json.RawMessage(`{"channel_id":"C123","thread_ts":"789.012"}`)
	slot.Launch.Subscriptions = []integrationstore.IntegrationSubscriptionAttachment{resource, primary}
	slot.Launch.IdempotencyKey = "secondary-launch"
	_, _, err = f.store.Integrations().
		AcceptIntegrationReceipt(
			f.ctx,
			integrationstore.VerifiedIntegrationReceipt{
				ProjectID:     testProjectID,
				IntegrationID: f.integration.ID,
				ReceiptKey:    "secondary-event",
				Payload:       []byte(`{}`),
			},
		)
	require.NoError(t, err)
	receipt, found, err := f.store.Integrations().
		ClaimIntegrationInbox(
			f.ctx,
			integrationstore.ClaimIntegrationInboxInput{
				ProjectID:     testProjectID,
				IntegrationID: f.integration.ID,
				LeaseDuration: time.Minute,
			},
		)
	require.NoError(t, err)
	require.True(t, found)
	plan, err := json.Marshal(map[string]executionstore.InboxLaunchSlot{"a": slot})
	require.NoError(t, err)
	require.NoError(
		t,
		f.store.Integrations().
			WithIntegrationInboxLease(
				f.ctx,
				receipt.Lease(),
				func(work *integrationstore.IntegrationInboxLeaseTx) error { return work.FreezePlan(f.ctx, plan) },
			),
	)
	blocker := integrationdb.BeginTx(t, f.ctx, f.store.pool)
	q := dbsqlc.New(blocker)
	require.NoError(
		t,
		q.LockProjectIntegrationLifecycleExclusive(
			f.ctx,
			dbsqlc.LockProjectIntegrationLifecycleExclusiveParams{IntegrationID: secondary.ID},
		),
	)
	done := integrationdb.RunAsync(func() (executionstore.LaunchAgentResult, error) {
		return f.store.Execution().AdmitInboxLaunchSlot(f.ctx, receipt.Lease(), "a")
	})
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.store.pool, "LockProjectIntegrationLifecycleShared", 1)
	lockCtx, cancel := context.WithTimeout(f.ctx, 2*time.Second)
	defer cancel()
	_, err = q.LockIntegrationInboxReceipt(
		lockCtx,
		dbsqlc.LockIntegrationInboxReceiptParams{ProjectID: testProjectID, ID: receipt.ID},
	)
	require.NoError(t, err, "secondary integration gate must precede receipt lock")
	require.NoError(t, blocker.Commit(f.ctx))
	result := integrationdb.AwaitSuccess(t, done, "secondary integration admission")
	require.Equal(t, slot.AgentID, result.Agent.ID)
	subscriptions := f.subscriptions(t, result.Agent.ID)
	require.Len(t, subscriptions, 2)
	integrationIDs := make([]uuid.UUID, 0, len(subscriptions))
	for _, subscription := range subscriptions {
		integrationIDs = append(integrationIDs, subscription.IntegrationID)
	}
	require.ElementsMatch(t, []uuid.UUID{f.integration.ID, secondary.ID}, integrationIDs)
}

func TestLaunchSubscriptionConversationLocksPrecedeLaunchKey(t *testing.T) {
	t.Parallel()
	f := newIntegrationActivationFixture(t)
	input := f.launchInput(f.profile.CurrentConfigID, "subscription-conversation-locks")
	first, second := f.attachment(), f.attachment()
	first.Conversation = json.RawMessage(`{"channel_id":"C100"}`)
	second.Conversation = json.RawMessage(`{"channel_id":"C900"}`)
	input.Subscriptions = []integrationstore.IntegrationSubscriptionAttachment{second, first}
	blocker := integrationdb.BeginTx(t, f.ctx, f.store.pool)
	require.NoError(t, integrationstore.LockConversationTx(f.ctx, blocker, testProjectID, f.integration.ID,
		integrationstore.ConversationAddress{Kind: "channel", Ref: "C100"}))
	done := integrationdb.RunAsync(func() (executionstore.LaunchAgentResult, error) {
		return f.store.Execution().LaunchAgent(f.ctx, input)
	})
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.store.pool, "LockIntegrationConversation", 1)
	lockCtx, cancel := context.WithTimeout(f.ctx, 2*time.Second)
	defer cancel()
	require.NoError(t, integrationstore.LockConversationTx(lockCtx, blocker, testProjectID, f.integration.ID,
		integrationstore.ConversationAddress{Kind: "channel", Ref: "C900"}),
		"attachment conversations must be locked in canonical order, independent of request order")
	require.NoError(t, dbsqlc.New(blocker).LockAgentLaunchIdempotencyKey(
		lockCtx, dbsqlc.LockAgentLaunchIdempotencyKeyParams{
			ProjectID: testProjectID, IdempotencyKey: input.IdempotencyKey,
		}), "all attachment conversations must be locked before the launch key")
	require.NoError(t, blocker.Commit(f.ctx))
	result := integrationdb.AwaitSuccess(t, done, "subscription-only launch")
	require.True(t, result.Created)
	require.Equal(t, f.profile.CurrentConfigID, result.Agent.CurrentConfigID)
	require.Len(t, f.subscriptions(t, result.Agent.ID), 2)
}

func TestSavedDerivedLaunchRetainsProfileAuthority(t *testing.T) {
	t.Parallel()
	f := newIntegrationActivationFixture(t)
	config, err := f.store.Execution().CreateAgentConfig(f.ctx, f.definition(t, "Saved derived capabilities"))
	require.NoError(t, err)
	input := f.launchInput(config.ID, "saved-derived")
	input.ProfileID = f.profile.ID
	input.DerivedBaseConfigID = config.ID
	_, err = f.store.Execution().LaunchAgent(f.ctx, input)
	require.ErrorIs(t, err, storeerr.ErrNotFound, "a derived config cannot claim to be a version of the profile")
	input.DerivedBaseConfigID = f.profile.CurrentConfigID
	result, err := f.store.Execution().LaunchAgent(f.ctx, input)
	require.NoError(t, err)
	require.True(t, result.Created)
	require.Equal(t, config.ID, result.Agent.CurrentConfigID)
	require.Equal(t, f.profile.ID, result.Agent.AgentProfileID)
}

func TestSavedDerivedLaunchRejectsForeignProjectConfig(t *testing.T) {
	t.Parallel()
	f := newIntegrationActivationFixture(t)
	foreign := seedAdditionalProjectForTest(t, f.ctx, f.store.pool, "derived-config")
	definition := f.definition(t, "Foreign config")
	definition.ProjectID = foreign
	_, err := f.store.Models().CreateProjectModelGrant(f.ctx, modelstore.CreateProjectModelGrantInput{
		OrgID: testOrgID, ProjectID: foreign, ConfiguredModelID: definition.ConfiguredModelID,
	})
	require.NoError(t, err)
	config, err := f.store.Execution().CreateAgentConfig(f.ctx, definition)
	require.NoError(t, err)
	input := f.launchInput(config.ID, "foreign-derived")
	input.ProfileID, input.DerivedBaseConfigID = f.profile.ID, f.profile.CurrentConfigID
	_, err = f.store.Execution().LaunchAgent(f.ctx, input)
	require.True(t, storeerr.IsNotFound(err), "%v", err)
}

func TestInboxConversationAuthorityRechecksSavedModel(t *testing.T) {
	t.Parallel()
	f := newInboxLaunchFixture(t, false, time.Minute, "a")
	slot := f.slots["a"]
	require.NoError(t, f.store.Execution().CheckInboxConversationAuthority(
		f.ctx, f.receipt.Lease(), "a", slot.Selection.Address,
	))
	modelID := f.profile.CurrentConfig.ConfiguredModelID
	grant, err := f.store.Models().GetActiveProjectModelGrantForConfiguredModel(f.ctx, testOrgID, testProjectID, modelID)
	require.NoError(t, err)
	_, err = f.store.Models().DeleteProjectModelGrant(f.ctx, testOrgID, testProjectID, grant.ID)
	require.NoError(t, err)
	_, err = f.store.Models().DeleteConfiguredModel(f.ctx, testOrgID, modelID)
	require.NoError(t, err)
	err = f.store.Execution().CheckInboxConversationAuthority(f.ctx, f.receipt.Lease(), "a", slot.Selection.Address)
	require.True(t, storeerr.IsNotFound(err), "%v", err)
	_, err = f.store.Execution().AdmitInboxLaunchSlot(f.ctx, f.receipt.Lease(), "a")
	require.True(t, storeerr.IsNotFound(err), "%v", err)
	f.assertAbsent(t, "a")
}

func TestInboxSavedConfigRechecksModelGrantAndRecovers(t *testing.T) {
	t.Parallel()
	for _, revoke := range []bool{true, false} {
		name := "tools_disabled"
		if revoke {
			name = "grant_revoked"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newInboxLaunchFixture(t, false, time.Minute, "a")
			slot := f.slots["a"]
			modelID := f.profile.CurrentConfig.ConfiguredModelID
			grant, err := f.store.Models().GetActiveProjectModelGrantForConfiguredModel(
				f.ctx, testOrgID, testProjectID, modelID,
			)
			require.NoError(t, err)
			if revoke {
				_, err = f.store.Models().DeleteProjectModelGrant(f.ctx, testOrgID, testProjectID, grant.ID)
			} else {
				disabled := false
				_, err = f.store.Models().UpdateProjectModelGrant(f.ctx, modelstore.UpdateProjectModelGrantInput{
					OrgID: testOrgID, ProjectID: testProjectID, ID: grant.ID,
					SupportsTools: patch.NullableBool{Set: true, Value: &disabled},
				})
			}
			require.NoError(t, err)
			require.Error(t, f.store.Execution().CheckInboxConversationAuthority(
				f.ctx, f.receipt.Lease(), "a", slot.Selection.Address,
			))
			_, err = f.store.Execution().AdmitInboxLaunchSlot(f.ctx, f.receipt.Lease(), "a")
			require.Error(t, err)
			f.assertAbsent(t, "a")
			if revoke {
				_, err = f.store.Models().CreateProjectModelGrant(f.ctx, modelstore.CreateProjectModelGrantInput{
					OrgID: testOrgID, ProjectID: testProjectID, ConfiguredModelID: modelID,
				})
			} else {
				_, err = f.store.Models().UpdateProjectModelGrant(f.ctx, modelstore.UpdateProjectModelGrantInput{
					OrgID: testOrgID, ProjectID: testProjectID, ID: grant.ID, SupportsTools: patch.NullableBool{Set: true},
				})
			}
			require.NoError(t, err)
			require.NoError(t, f.store.Execution().CheckInboxConversationAuthority(
				f.ctx, f.receipt.Lease(), "a", slot.Selection.Address,
			))
			result, err := f.store.Execution().AdmitInboxLaunchSlot(f.ctx, f.receipt.Lease(), "a")
			require.NoError(t, err)
			require.True(t, result.Created)
			restored, err := f.store.Models().GetActiveProjectModelGrantForConfiguredModel(
				f.ctx, testOrgID, testProjectID, modelID,
			)
			require.NoError(t, err)
			_, err = f.store.Models().DeleteProjectModelGrant(f.ctx, testOrgID, testProjectID, restored.ID)
			require.NoError(t, err)
			replay, err := f.store.Execution().AdmitInboxLaunchSlot(f.ctx, f.receipt.Lease(), "a")
			require.NoError(t, err)
			require.Equal(t, result.Agent.ID, replay.Agent.ID)
		})
	}
}
