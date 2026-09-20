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
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestLaunchInitialContentOriginAndReplay(t *testing.T) {
	t.Parallel()
	f := newAppActivationFixture(t)
	definition := f.definition(t, "Initial origin", map[string]agentconfig.AppCapabilityCompiled{
		"chat__thread_messages": f.listener(),
	})
	input := f.launchInput(uuid.Nil, "initial-origin")
	input.DerivedConfig = &definition
	input.InitialInput = &executionstore.LaunchInitialInput{
		ContentBlocks: json.RawMessage(
			`[{"type":"text","text":"First","metadata":{"part":"1"}},{"type":"text","text":"Second"}]`,
		),
		Metadata: json.RawMessage(`{"event":"message","timestamp":"123.456"}`),
		Actor: &executionstore.ActorParams{
			Provider:         "slack",
			ProviderTenantID: f.app.ProviderTenantID,
			ProviderUserID:   "U_INITIAL",
		},
		Origin: &executionstore.LaunchInputOrigin{
			AppID:       f.app.ID,
			Address:     integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:123.456"},
			DisplayName: "Initial thread",
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
	require.Equal(t, integrationstore.TargetAttribution, launch.IntegrationTarget.RoutingRole)
	require.Equal(t, "integration:slack:"+f.app.ID.String(), launch.AgentInput.IdempotencyScope)
	require.Equal(t, input.InitialInput.SemanticEventKey, launch.AgentInput.InputIdempotencyKey)
	require.Len(t, f.listeners(t, launch.Agent.ID), 1)
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
			f := newAppActivationFixture(t)
			definition := f.definition(
				t,
				"Failed initial input",
				map[string]agentconfig.AppCapabilityCompiled{"chat__thread_messages": f.listener()},
			)
			input := f.launchInput(uuid.Nil, "invalid-initial")
			input.DerivedConfig = &definition
			input.InitialInput = &executionstore.LaunchInitialInput{
				ContentBlocks: json.RawMessage(`[{"type":"text","text":"test"}]`),
				Actor: &executionstore.ActorParams{
					Provider:         "slack",
					ProviderTenantID: f.app.ProviderTenantID,
					ProviderUserID:   "U_INITIAL",
				},
				Origin: &executionstore.LaunchInputOrigin{
					AppID:   f.app.ID,
					Address: integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:123.456"},
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
	f := newAppActivationFixture(t)
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
	appActivationFixture
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
	f := inboxLaunchFixture{
		appActivationFixture: newAppActivationFixture(t),
		slots:                map[string]executionstore.InboxLaunchSlot{},
	}
	setup := integrationstore.SaveProjectAppInput{
		OrgID:        testOrgID,
		ProjectID:    testProjectID,
		Name:         f.app.Name,
		DefinitionID: appdefinition.Slack,
		Settings: integrationstore.ProjectAppSettings{
			Launcher: &integrationstore.AppLauncher{Trigger: "mention", ScopeKind: "channel", ScopeRef: "C123"},
		},
	}
	for _, key := range keys {
		setup.Settings.Launcher.Slots = append(
			setup.Settings.Launcher.Slots,
			integrationstore.AppLaunchSlot{Key: key, AgentProfileID: &f.profile.ID},
		)
	}
	var err error
	f.app, err = f.store.Integrations().UpdateProjectApp(f.ctx, f.app.ID, setup)
	require.NoError(t, err)
	_, _, err = f.store.Integrations().
		AcceptIntegrationReceipt(
			f.ctx,
			integrationstore.VerifiedIntegrationReceipt{
				ProjectID:  testProjectID,
				AppID:      f.app.ID,
				ReceiptKey: "launch-event",
				Payload:    []byte(`{"event":"message"}`),
			},
		)
	require.NoError(t, err)
	var found bool
	f.receipt, found, err = f.store.Integrations().
		ClaimIntegrationInbox(
			f.ctx,
			integrationstore.ClaimIntegrationInboxInput{
				ProjectID:     testProjectID,
				AppID:         f.app.ID,
				LeaseDuration: leaseDuration,
			},
		)
	require.NoError(t, err)
	require.True(t, found)
	for _, key := range keys {
		plannedID, err := uuid.NewV7()
		require.NoError(t, err)
		definition := f.definition(
			t,
			"Frozen initial config "+key,
			map[string]agentconfig.AppCapabilityCompiled{"chat__thread_messages": f.listener()},
		)
		launch := f.launchInput(uuid.Nil, "inbox-launch-"+key)
		launch.DerivedConfig, launch.ProfileID = &definition, f.profile.ID
		launch.DerivedBaseConfigID = f.profile.CurrentConfigID
		launch.InitialInput = &executionstore.LaunchInitialInput{
			ContentBlocks: json.RawMessage(
				`[{"type":"text","text":"First event"}]`,
			),
			Metadata: json.RawMessage(`{"event":"frozen-first"}`),
			Actor: &executionstore.ActorParams{
				Provider:         "slack",
				ProviderTenantID: f.app.ProviderTenantID,
				ProviderUserID:   "U_LAUNCH",
			},
			Origin: &executionstore.LaunchInputOrigin{
				AppID:   f.app.ID,
				Address: integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:123.456"},
			},
			SemanticEventKey: "message:123.456",
			DeliveryMode:     executionstore.DeliveryModeSteering,
		}
		slot := executionstore.InboxLaunchSlot{
			AgentID:     plannedID,
			ListenerKey: "chat__thread_messages",
			Launch:      launch,
			Selection: integrationstore.InboxAppSelection{
				AppID:   f.app.ID,
				Address: launch.InitialInput.Origin.Address,
				Slot:    key,
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

func (f inboxLaunchFixture) assertAbsent(t *testing.T, key string) {
	t.Helper()
	slot := f.slots[key]
	for _, check := range []struct {
		query string
		arg   any
	}{
		{`SELECT count(*) FROM agents WHERE id=$1`, slot.AgentID},
		{
			`SELECT count(*) FROM agent_configs WHERE effective_definition_hash=$1`,
			slot.Launch.DerivedConfig.EffectiveDefinitionHash,
		},
		{`SELECT count(*) FROM integration_targets WHERE agent_id=$1`, slot.AgentID},
		{`SELECT count(*) FROM agent_listeners WHERE agent_id=$1`, slot.AgentID},
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
	// No blobs/configs/agents are inserted by freezing or failed admission.
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
	require.Equal(t, integrationstore.TargetSelected, created.IntegrationTarget.RoutingRole)
	require.Equal(t, f.app.ID, created.IntegrationTarget.AppID)
	require.Equal(t, "a", created.IntegrationTarget.SelectionSlot)
	require.Equal(t, created.IntegrationTarget.ID, created.AgentInput.IntegrationTargetID)
	require.Len(t, created.Artifacts, 1)
	require.Equal(t, f.slots["a"].ArtifactIDs[0], created.Artifacts[0].ID)
	require.Equal(t, created.Agent.ID, created.Artifacts[0].AgentID)
	require.JSONEq(t, string(f.slots["a"].Launch.InitialInput.ContentBlocks), string(created.InputContentBlocks))
	require.Len(t, f.listeners(t, created.Agent.ID), 2)
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
		ChangeAgentConfig(f.ctx, f.changeInput(t, created.Agent.ID, "After launch", nil, "after-launch"))
	require.NoError(t, err)
	replayed, err := f.store.Execution().AdmitInboxLaunchSlot(f.ctx, f.receipt.Lease(), "a")
	require.NoError(t, err)
	require.False(t, replayed.Created)
	require.Equal(t, changed.AgentConfig.ID, replayed.Agent.CurrentConfigID)
	require.Empty(t, f.listeners(t, created.Agent.ID), "replay cannot rematerialize old resources")
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
	require.Eventually(
		t,
		func() bool { return time.Now().After(*f.receipt.ClaimExpiresAt) },
		3*time.Second,
		10*time.Millisecond,
	)
	require.NoError(t, blocker.Commit(f.ctx))
	result := integrationdb.Await(t, done, "expired admission")
	require.ErrorIs(t, result.Err, integrationstore.ErrIntegrationInboxLeaseLost)
	f.assertAbsent(t, "a")
	// Prepared blob facts remain available for recovery; admission performs no cleanup.
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
				AppID:         f.app.ID,
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
		f.slots["a"].Launch.DerivedConfig.EffectiveDefinitionHash,
		admitted.AgentConfig.EffectiveDefinitionHash,
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

func TestInboxLaunchRetainsFrozenMembershipAcrossAppEdit(t *testing.T) {
	t.Parallel()
	f := newInboxLaunchFixture(t, true, time.Minute, "a", "b")
	f.prepare(t, "a")
	first, err := f.store.Execution().AdmitInboxLaunchSlot(f.ctx, f.receipt.Lease(), "a")
	require.NoError(t, err)
	_, err = f.store.Execution().AdmitInboxLaunchSlot(f.ctx, f.receipt.Lease(), "b")
	require.Error(t, err)
	f.assertAbsent(t, "b")
	settings := f.app.Settings
	settings.Launcher.Slots[1].Key = "c"
	_, err = f.store.Integrations().
		UpdateProjectApp(
			f.ctx,
			f.app.ID,
			integrationstore.SaveProjectAppInput{
				OrgID:        testOrgID,
				ProjectID:    testProjectID,
				Name:         f.app.Name,
				DefinitionID: f.app.DefinitionID,
				Settings:     settings,
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
		f.slots["b"].Launch.DerivedConfig.EffectiveDefinitionHash,
		second.AgentConfig.EffectiveDefinitionHash,
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

func TestInboxLaunchLocksSecondaryAppBeforeReceipt(t *testing.T) {
	t.Parallel()
	f := newInboxLaunchFixture(t, false, time.Minute, "a")
	credential := createIntegrationCredential(t, f.ctx, f.store, testProjectID, f.user.ID, "secondary-launch")
	secondary := mustCreateProjectApp(
		t,
		f.ctx,
		f.store,
		slackProjectAppSetupInput(f.profile.ID, uuid.Nil, f.user.ID, credential, "A_SECONDARY", "T_SECONDARY"),
	)
	resource := f.listener()
	resource.AppID = publicResourceID(publicid.KindProjectApp, secondary.ID)
	definition := f.definition(
		t,
		"Secondary resource gate",
		map[string]agentconfig.AppCapabilityCompiled{
			"chat__thread_messages":              f.listener(),
			secondary.Name + "__thread_messages": resource,
		},
	)
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
	slot.Launch.DerivedConfig = &definition
	slot.Launch.IdempotencyKey = "secondary-launch"
	_, _, err = f.store.Integrations().
		AcceptIntegrationReceipt(
			f.ctx,
			integrationstore.VerifiedIntegrationReceipt{
				ProjectID:  testProjectID,
				AppID:      f.app.ID,
				ReceiptKey: "secondary-event",
				Payload:    []byte(`{}`),
			},
		)
	require.NoError(t, err)
	receipt, found, err := f.store.Integrations().
		ClaimIntegrationInbox(
			f.ctx,
			integrationstore.ClaimIntegrationInboxInput{
				ProjectID:     testProjectID,
				AppID:         f.app.ID,
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
		q.LockProjectAppLifecycleExclusive(
			f.ctx,
			dbsqlc.LockProjectAppLifecycleExclusiveParams{AppID: secondary.ID},
		),
	)
	done := integrationdb.RunAsync(func() (executionstore.LaunchAgentResult, error) {
		return f.store.Execution().AdmitInboxLaunchSlot(f.ctx, receipt.Lease(), "a")
	})
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.store.pool, "LockProjectAppLifecycleShared", 1)
	lockCtx, cancel := context.WithTimeout(f.ctx, 2*time.Second)
	defer cancel()
	_, err = q.LockIntegrationInboxReceipt(
		lockCtx,
		dbsqlc.LockIntegrationInboxReceiptParams{ProjectID: testProjectID, ID: receipt.ID},
	)
	require.NoError(t, err, "secondary app gate must precede receipt lock")
	require.NoError(t, blocker.Commit(f.ctx))
	result := integrationdb.AwaitSuccess(t, done, "secondary app admission")
	require.Equal(t, slot.AgentID, result.Agent.ID)
	require.Len(t, f.listeners(t, result.Agent.ID), 3)
}
