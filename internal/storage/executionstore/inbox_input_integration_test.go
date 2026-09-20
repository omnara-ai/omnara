//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func inboxInputApp(t *testing.T, f appActivationFixture, provider string) integrationstore.ProjectAppRecord {
	t.Helper()
	require.Equal(t, "github", provider)
	secret, _, err := f.store.Secrets().CreateSecret(f.ctx, secretstore.CreateSecretInput{
		OrgID: testOrgID, OwnerKind: secretstore.SecretOwnerProject, OwnerProjectID: testProjectID,
		Name: "github-" + uuid.NewString(), Actor: userPrincipal(f.user.ID),
		Material: secrets.GitHubAppCredentialsMaterial{
			AppID:         "123",
			PrivateKey:    "kernel-test-private-key",
			WebhookSecret: "kernel-test-webhook",
		},
	})
	require.NoError(t, err)
	app, err := f.store.Integrations().CreateProjectApp(f.ctx, integrationstore.SaveProjectAppInput{
		OrgID: testOrgID, ProjectID: testProjectID, Name: "github", DefinitionID: appdefinition.GitHub,
	})
	require.NoError(t, err)
	app, err = f.store.Integrations().ConfigureProjectApp(f.ctx, integrationstore.ConfigureProjectAppInput{
		OrgID: testOrgID, ProjectID: testProjectID, AppID: app.ID, InstalledByUserID: f.user.ID,
		Provider: provider, ProviderTenantID: "123", ProviderAccountRef: "456", CredentialAppID: 123,
		CredentialSecretID: secret.ID, CredentialVersionID: secret.CurrentVersionID, ExpectedSetupRevision: app.SetupRevision,
		ProviderIdentity: json.RawMessage(`{"bot_user_id":789,"bot_login":"kernel-test[bot]"}`),
	})
	require.NoError(t, err)
	return app
}

func (f appInteractionFixture) activation() appActivationFixture {
	return appActivationFixture{ctx: f.ctx, store: f.store, user: f.user, profile: f.profile, app: f.app}
}

func inboxInputPlan(
	agentID uuid.UUID,
	app integrationstore.ProjectAppRecord,
	event string,
) executionstore.InboxInputSlot {
	return executionstore.InboxInputSlot{AgentID: agentID, Input: executionstore.CreateAgentContentInputInput{
		Origin: &executionstore.AgentInputOrigin{
			AppID:   app.ID,
			Address: integrationstore.ConversationAddress{Kind: "pull_request", Ref: "123#42"},
		},
		Actor: &executionstore.ActorParams{
			Provider:         app.Provider,
			ProviderTenantID: app.ProviderTenantID,
			ProviderUserID:   "participant",
		},
		ContentBlocks:          json.RawMessage(`[{"type":"text","text":"Please address this review"}]`),
		Metadata:               json.RawMessage(`{"source":"review"}`),
		IdempotencyKey:         event,
		DeliveryMode:           executionstore.DeliveryModeSteering,
		CancelOpenInteractions: true,
	}}
}

func freezeInboxInput(
	t *testing.T,
	f appActivationFixture,
	slot executionstore.InboxInputSlot,
	key string,
	lease time.Duration,
) integrationstore.IntegrationInboxRecord {
	t.Helper()
	_, _, err := f.store.Integrations().
		AcceptIntegrationReceipt(
			f.ctx,
			integrationstore.VerifiedIntegrationReceipt{
				ProjectID:  testProjectID,
				AppID:      slot.Input.Origin.AppID,
				ReceiptKey: key,
				Payload:    []byte(`{"verified":true}`),
			},
		)
	require.NoError(t, err)
	receipt, found, err := f.store.Integrations().
		ClaimIntegrationInbox(
			f.ctx,
			integrationstore.ClaimIntegrationInboxInput{
				ProjectID:     testProjectID,
				AppID:         slot.Input.Origin.AppID,
				LeaseDuration: lease,
			},
		)
	require.NoError(t, err)
	require.True(t, found)
	plan, err := json.Marshal(map[string]executionstore.InboxInputSlot{"recipient": slot})
	require.NoError(t, err)
	require.NoError(
		t,
		f.store.Integrations().
			WithIntegrationInboxLease(
				f.ctx,
				receipt.Lease(),
				func(w *integrationstore.IntegrationInboxLeaseTx) error { return w.FreezePlan(f.ctx, plan) },
			),
	)
	return receipt
}

func prepareInboxInput(
	t *testing.T,
	f appActivationFixture,
	receipt integrationstore.IntegrationInboxRecord,
	slot executionstore.InboxInputSlot,
) {
	t.Helper()
	var prepared executionstore.InboxInputPreparation
	for _, id := range slot.ArtifactIDs {
		prepared.Artifacts = append(
			prepared.Artifacts,
			artifactstore.PreparedArtifact{
				ID:          id,
				ContentType: "text/plain",
				Filename:    "review.txt",
				Digest:      blobstore.ContentDigest([]byte("review")),
				SizeBytes:   6,
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
				receipt.Lease(),
				func(w *integrationstore.IntegrationInboxLeaseTx) error { return w.PrepareSlot(f.ctx, "recipient", raw) },
			),
	)
}

func withInboxFile(t *testing.T, slot executionstore.InboxInputSlot) executionstore.InboxInputSlot {
	t.Helper()
	id, err := uuid.NewV7()
	require.NoError(t, err)
	slot.ArtifactIDs = []uuid.UUID{id}
	slot.Input.ContentBlocks = json.RawMessage(
		`[{"type":"text","text":"Review attached"},{"type":"media_ref","artifact_id":"` + id.String() + `"}]`,
	)
	return slot
}

func TestInboxInputGitHubCommentsSteerAndCancelAcrossProviders(t *testing.T) {
	t.Parallel()
	for _, event := range []string{"issue_comment:100", "pull_request_review_comment:200"} {
		t.Run(event, func(t *testing.T) {
			t.Parallel()
			f := newAppInteractionFixture(t)
			calls := createToolCallBatchForProcessTest(
				t,
				f.ctx,
				f.process,
				"cross-provider-prompts",
				[]processToolCallBatchItem{
					builtInProcessToolCallBatchItem("first", "ask_question"),
					builtInProcessToolCallBatchItem("second", "ask_question"),
					builtInProcessToolCallBatchItem("later", "ask_question"),
				},
			)
			f.selectOrigin(t, f.a.ID)
			firstPrompt := createQuestionInteractionForTest(t, f.ctx, f.process, calls[0])
			f.selectOrigin(t, f.b.ID)
			secondPrompt := createQuestionInteractionForTest(t, f.ctx, f.process, calls[1])
			app := inboxInputApp(t, f.activation(), executionstore.ActorProviderGitHub)
			slot := inboxInputPlan(f.process.AgentID, app, event)
			receipt := freezeInboxInput(t, f.activation(), slot, "first", time.Minute)
			before, err := f.store.Execution().GetAgentInProject(f.ctx, testProjectID, f.process.AgentID)
			require.NoError(t, err)
			result, err := f.store.Execution().AdmitInboxInputSlot(f.ctx, receipt.Lease(), "recipient")
			require.NoError(t, err)
			require.True(t, result.Created)
			require.ElementsMatch(t, []uuid.UUID{firstPrompt.ID, secondPrompt.ID}, result.CanceledInteractionIDs)
			for _, id := range result.CanceledInteractionIDs {
				prompt := f.read(t, id)
				require.Equal(t, executionstore.AgentInteractionStateCanceled, prompt.State)
				require.Equal(t, result.AgentInput.ID, prompt.ResolvedByInputID)
			}
			require.Equal(t, executionstore.DeliveryModeSteering, result.AgentInput.DeliveryMode)
			require.Equal(t, integrationstore.TargetAttribution, result.IntegrationTarget.RoutingRole)
			require.Equal(t, app.ID, result.IntegrationTarget.AppID)
			actor, err := f.store.Execution().GetActor(f.ctx, testProjectID, result.AgentInput.ActorID)
			require.NoError(t, err)
			require.Equal(t, "github", actor.Provider)
			after, err := f.store.Execution().GetAgentInProject(f.ctx, testProjectID, f.process.AgentID)
			require.NoError(t, err)
			require.Equal(t, before.CurrentConfigID, after.CurrentConfigID)
			require.Empty(t, f.activation().listeners(t, f.process.AgentID))
			selection, err := f.store.Execution().GetInteractionSelection(f.ctx, testProjectID, f.process.AgentID)
			require.NoError(t, err)
			require.Equal(
				t,
				executionstore.InteractionSelection{},
				selection,
				"unsupported GitHub handler chooses dashboard",
			)

			// Repeated provider callbacks and overlapping subscriptions deduplicate
			// across receipt/slot identity and cannot cancel newer prompts.
			selected := f.selectOrigin(t, f.a.ID)
			newPrompt := createQuestionInteractionForTest(t, f.ctx, f.process, calls[2])
			duplicate := freezeInboxInput(t, f.activation(), slot, "other-callback", time.Minute)
			replayed, err := f.store.Execution().AdmitInboxInputSlot(f.ctx, duplicate.Lease(), "recipient")
			require.NoError(t, err)
			require.False(t, replayed.Created)
			require.Equal(t, result.AgentInput.ID, replayed.AgentInput.ID)
			require.Empty(t, replayed.CanceledInteractionIDs)
			require.Equal(t, executionstore.AgentInteractionStateOpen, f.read(t, newPrompt.ID).State)
			selection, err = f.store.Execution().GetInteractionSelection(f.ctx, testProjectID, f.process.AgentID)
			require.NoError(t, err)
			require.Equal(t, selected, selection)
			require.NoError(
				t,
				f.store.Integrations().
					WithIntegrationInboxLease(
						f.ctx,
						duplicate.Lease(),
						func(w *integrationstore.IntegrationInboxLeaseTx) error { return w.Complete(f.ctx) },
					),
			)

			// A GitHub commit is ordinary queued activity. Delivery policy is
			// explicit and remains independent of its provider and origin.
			slot.Input.IdempotencyKey = "commit:abcdef"
			slot.Input.DeliveryMode = executionstore.DeliveryModeQueued
			slot.Input.CancelOpenInteractions = false
			queued := freezeInboxInput(t, f.activation(), slot, "commit-callback", time.Minute)
			result, err = f.store.Execution().AdmitInboxInputSlot(f.ctx, queued.Lease(), "recipient")
			require.NoError(t, err)
			require.Equal(t, executionstore.DeliveryModeQueued, result.AgentInput.DeliveryMode)
			require.Empty(t, result.CanceledInteractionIDs)
			require.Equal(t, executionstore.AgentInteractionStateOpen, f.read(t, newPrompt.ID).State)
			// Steering and prompt cancellation remain independent policies.
			slot.Input.DeliveryMode, slot.Input.IdempotencyKey = executionstore.DeliveryModeSteering, "steer-without-cancel"
			steering := freezeInboxInput(t, f.activation(), slot, "steering-callback", time.Minute)
			result, err = f.store.Execution().AdmitInboxInputSlot(f.ctx, steering.Lease(), "recipient")
			require.NoError(t, err)
			require.Empty(t, result.CanceledInteractionIDs)
			require.Equal(t, executionstore.AgentInteractionStateOpen, f.read(t, newPrompt.ID).State)
		})
	}
}

func TestInboxInputConcurrentMediaAndCompletedReplay(t *testing.T) {
	t.Parallel()
	f := newAppActivationFixture(t)
	agent, err := f.store.Execution().LaunchAgent(f.ctx, f.launchInput(f.profile.CurrentConfigID, "existing-recipient"))
	require.NoError(t, err)
	slot := withInboxFile(t, inboxInputPlan(agent.Agent.ID, f.app, "message:file"))
	slot.Input.Origin.Address = integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:111.222"}
	first := freezeInboxInput(t, f, slot, "file-one", time.Minute)
	second := freezeInboxInput(t, f, slot, "file-two", time.Minute)
	prepareInboxInput(t, f, first, slot)
	prepareInboxInput(t, f, second, slot)
	start := make(chan struct{})
	admit := func(receipt integrationstore.IntegrationInboxRecord) func() (executionstore.InboxInputResult, error) {
		return func() (executionstore.InboxInputResult, error) {
			<-start
			return f.store.Execution().AdmitInboxInputSlot(f.ctx, receipt.Lease(), "recipient")
		}
	}
	one, two := integrationdb.RunAsync(admit(first)), integrationdb.RunAsync(admit(second))
	close(start)
	a, b := integrationdb.AwaitSuccess(t, one, "first event"), integrationdb.AwaitSuccess(t, two, "duplicate event")
	require.NotEqual(t, a.Created, b.Created)
	require.Equal(t, a.AgentInput.ID, b.AgentInput.ID)
	for _, table := range []string{"artifacts", "integration_targets"} {
		var count int
		require.NoError(
			t,
			f.store.pool.QueryRow(f.ctx, "SELECT count(*) FROM "+table+" WHERE agent_id=$1", agent.Agent.ID).
				Scan(&count),
		)
		require.Equal(t, 1, count)
	}
	for _, receipt := range []integrationstore.IntegrationInboxRecord{first, second} {
		require.NoError(
			t,
			f.store.Integrations().
				WithIntegrationInboxLease(
					f.ctx,
					receipt.Lease(),
					func(w *integrationstore.IntegrationInboxLeaseTx) error { return w.Complete(f.ctx) },
				),
		)
	}
	f.disable(t)
	replayed, err := f.store.Execution().AdmitInboxInputSlot(f.ctx, first.Lease(), "recipient")
	require.NoError(t, err)
	require.False(t, replayed.Created)
	require.Equal(t, a.AgentInput.ID, replayed.AgentInput.ID)
}

func TestInboxInputExpiredLeaseRollsBackOriginMediaAndCancellation(t *testing.T) {
	t.Parallel()
	f := newAppInteractionFixture(t)
	selected := f.selectOrigin(t, f.a.ID)
	prompt := f.question(t)
	slot := withInboxFile(t, inboxInputPlan(f.process.AgentID, f.app, "expire-file"))
	slot.Input.Origin.Address = integrationstore.ConversationAddress{Kind: "thread", Ref: "C456:999.888"}
	receipt := freezeInboxInput(t, f.activation(), slot, "expire", 2*time.Second)
	prepareInboxInput(t, f.activation(), receipt, slot)
	blocker := integrationdb.BeginTx(t, f.ctx, f.store.pool)
	_, err := dbsqlc.New(blocker).
		LockAgentInProject(f.ctx, dbsqlc.LockAgentInProjectParams{ProjectID: testProjectID, ID: f.process.AgentID})
	require.NoError(t, err)
	done := integrationdb.RunAsync(func() (executionstore.InboxInputResult, error) {
		return f.store.Execution().AdmitInboxInputSlot(f.ctx, receipt.Lease(), "recipient")
	})
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.store.pool, "LockAgentInProject", 1)
	require.Eventually(
		t,
		func() bool { return time.Now().After(*receipt.ClaimExpiresAt) },
		3*time.Second,
		10*time.Millisecond,
	)
	require.NoError(t, blocker.Commit(f.ctx))
	require.ErrorIs(t, integrationdb.Await(t, done, "expired input").Err, integrationstore.ErrIntegrationInboxLeaseLost)
	require.Equal(t, executionstore.AgentInteractionStateOpen, f.read(t, prompt.ID).State)
	current, err := f.store.Execution().GetInteractionSelection(f.ctx, testProjectID, f.process.AgentID)
	require.NoError(t, err)
	require.Equal(t, selected, current)
	var count int
	require.NoError(
		t,
		f.store.pool.QueryRow(f.ctx, `SELECT count(*) FROM artifacts WHERE id=$1`, slot.ArtifactIDs[0]).Scan(&count),
	)
	require.Zero(t, count)
	require.NoError(
		t,
		f.store.pool.QueryRow(
			f.ctx,
			`SELECT count(*) FROM integration_targets WHERE agent_id=$1 AND provider_ref=$2`,
			f.process.AgentID,
			slot.Input.Origin.Address.Ref,
		).
			Scan(
				&count,
			),
	)
	require.Zero(t, count)
	require.NoError(
		t,
		f.store.pool.QueryRow(
			f.ctx,
			`SELECT count(*) FROM agent_inputs WHERE agent_id=$1 AND input_idempotency_key=$2`,
			f.process.AgentID,
			slot.Input.IdempotencyKey,
		).
			Scan(
				&count,
			),
	)
	require.Zero(t, count)
	record, err := f.store.Integrations().GetIntegrationInbox(f.ctx, testProjectID, receipt.ID)
	require.NoError(t, err)
	var progress map[string]map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(record.Progress, &progress))
	require.Empty(t, progress["recipient"]["committed"])
	require.NotEmpty(t, progress["recipient"]["prepared"])
}

func TestInboxInputAppGateBeforeReceiptAndAgent(t *testing.T) {
	t.Parallel()
	f := newAppActivationFixture(t)
	agent, err := f.store.Execution().LaunchAgent(f.ctx, f.launchInput(f.profile.CurrentConfigID, "lock-recipient"))
	require.NoError(t, err)
	slot := inboxInputPlan(agent.Agent.ID, f.app, "message:lock")
	slot.Input.Origin.Address = integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:111.222"}
	receipt := freezeInboxInput(t, f, slot, "lock-event", time.Minute)
	blocker := integrationdb.BeginTx(t, f.ctx, f.store.pool)
	q := dbsqlc.New(blocker)
	require.NoError(
		t,
		q.LockProjectAppLifecycleExclusive(
			f.ctx,
			dbsqlc.LockProjectAppLifecycleExclusiveParams{AppID: f.app.ID},
		),
	)
	done := integrationdb.RunAsync(func() (executionstore.InboxInputResult, error) {
		return f.store.Execution().AdmitInboxInputSlot(f.ctx, receipt.Lease(), "recipient")
	})
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.store.pool, "LockProjectAppLifecycleShared", 1)
	ctx, cancel := context.WithTimeout(f.ctx, time.Second)
	defer cancel()
	_, err = q.LockIntegrationInboxReceipt(
		ctx,
		dbsqlc.LockIntegrationInboxReceiptParams{ProjectID: testProjectID, ID: receipt.ID},
	)
	require.NoError(t, err)
	_, err = q.LockAgentInProject(ctx, dbsqlc.LockAgentInProjectParams{ProjectID: testProjectID, ID: agent.Agent.ID})
	require.NoError(t, err)
	require.NoError(t, blocker.Commit(f.ctx))
	require.True(t, integrationdb.AwaitSuccess(t, done, "ordered input admission").Created)
}

func TestInboxInputSelectsAuthorizedHandlerAndOriginlessInputPreservesIt(t *testing.T) {
	t.Parallel()
	f := newAppInteractionFixture(t)
	slot := inboxInputPlan(f.process.AgentID, f.otherApp, "handler-origin")
	slot.Input.Origin.Address = integrationstore.ConversationAddress{Kind: "thread", Ref: f.b.ProviderRef}
	slot.Input.CancelOpenInteractions = false
	receipt := freezeInboxInput(t, f.activation(), slot, "handler-origin", time.Minute)
	result, err := f.store.Execution().AdmitInboxInputSlot(f.ctx, receipt.Lease(), "recipient")
	require.NoError(t, err)
	require.True(t, result.Created)
	selected, err := f.store.Execution().GetInteractionSelection(f.ctx, testProjectID, f.process.AgentID)
	require.NoError(t, err)
	require.Equal(t, "other", selected.HandlerKey)
	require.Equal(t, f.b.ID, selected.IntegrationTargetID)
	require.JSONEq(t, `{"thread_ts":"333.444"}`, string(selected.Args))
	_, _, _, err = f.store.Execution().CreateAgentContentInput(f.ctx, executionstore.CreateAgentContentInputInput{
		ProjectID: testProjectID, AgentID: f.process.AgentID, Actor: mustOmnaraActorParams(t, f.user.ID),
		ContentBlocks: json.RawMessage(`[{"type":"text","text":"Dashboard note"}]`), IdempotencyKey: "originless",
	})
	require.NoError(t, err)
	after, err := f.store.Execution().GetInteractionSelection(f.ctx, testProjectID, f.process.AgentID)
	require.NoError(t, err)
	require.Equal(t, selected, after)
}

func TestOrdinaryContentInputExternalActorPreservesSelectionAndRejectsOrigin(t *testing.T) {
	t.Parallel()
	f := newAppInteractionFixture(t)
	f.selectOrigin(t, f.a.ID)
	input := executionstore.CreateAgentContentInputInput{
		ProjectID: testProjectID,
		AgentID:   f.process.AgentID,
		Actor: &executionstore.ActorParams{
			Provider: executionstore.ActorProviderExternal, ProviderUserID: "support-user",
		},
		IdempotencyKey: "external-actor-independent",
		ContentBlocks:  json.RawMessage(`[{"type":"text","text":"External application input"}]`),
	}
	first, _, created, err := f.store.Execution().CreateAgentContentInput(f.ctx, input)
	require.NoError(t, err)
	require.True(t, created)
	actor, err := f.store.Execution().GetActor(f.ctx, testProjectID, first.ActorID)
	require.NoError(t, err)
	require.Equal(t, executionstore.ActorProviderExternal, actor.Provider)
	require.Equal(t, uuid.Nil, first.IntegrationTargetID)
	selection, err := f.store.Execution().GetInteractionSelection(f.ctx, testProjectID, f.process.AgentID)
	require.NoError(t, err)
	require.Equal(t, "chat", selection.HandlerKey)
	require.Equal(t, f.a.ID, selection.IntegrationTargetID)
	f.selectOrigin(t, f.b.ID)
	replayed, _, created, err := f.store.Execution().CreateAgentContentInput(f.ctx, input)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, first.ID, replayed.ID)
	selection, err = f.store.Execution().GetInteractionSelection(f.ctx, testProjectID, f.process.AgentID)
	require.NoError(t, err)
	require.Equal(t, "other", selection.HandlerKey)
	input.ContentBlocks = json.RawMessage(`[{"type":"text","text":"Changed retry"}]`)
	_, _, _, err = f.store.Execution().CreateAgentContentInput(f.ctx, input)
	require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
	input.Origin = &executionstore.AgentInputOrigin{
		AppID:   f.app.ID,
		Address: integrationstore.ConversationAddress{Kind: "thread", Ref: f.a.ProviderRef},
	}
	input.IdempotencyKey = "forged-native"
	_, _, _, err = f.store.Execution().CreateAgentContentInput(f.ctx, input)
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
	input.Origin, input.IntegrationTargetID = nil, f.a.ID
	_, _, _, err = f.store.Execution().CreateAgentContentInput(f.ctx, input)
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest, "target ID cannot bypass public origin validation")
}

func TestInboxInputInvalidPreparationOrActorLeavesNoAdmission(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{
		"missing-preparation", "substituted-artifact", "wrong-tenant", "external-actor", "queued-cancellation", "wrong-project",
	} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			f := newAppActivationFixture(t)
			agent, err := f.store.Execution().
				LaunchAgent(f.ctx, f.launchInput(f.profile.CurrentConfigID, "invalid-recipient"))
			require.NoError(t, err)
			slot := withInboxFile(t, inboxInputPlan(agent.Agent.ID, f.app, "invalid-file"))
			slot.Input.Origin.Address = integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:111.222"}
			switch scenario {
			case "external-actor":
				slot.Input.Actor.Provider = executionstore.ActorProviderExternal
			case "wrong-tenant":
				slot.Input.Actor.ProviderTenantID = "another-account"
			case "queued-cancellation":
				slot.Input.DeliveryMode = executionstore.DeliveryModeQueued
			case "wrong-project":
				slot.Input.ProjectID = uuid.New()
			}
			receipt := freezeInboxInput(t, f, slot, scenario, time.Minute)
			if scenario != "missing-preparation" {
				preparation := slot
				if scenario == "substituted-artifact" {
					preparation.ArtifactIDs = []uuid.UUID{uuid.Must(uuid.NewV7())}
				}
				prepareInboxInput(t, f, receipt, preparation)
			}
			_, err = f.store.Execution().AdmitInboxInputSlot(f.ctx, receipt.Lease(), "recipient")
			require.Error(t, err)
			for _, check := range []struct {
				query string
				arg   any
			}{
				{`SELECT count(*) FROM integration_targets WHERE agent_id=$1`, agent.Agent.ID},
				{`SELECT count(*) FROM artifacts WHERE agent_id=$1`, agent.Agent.ID},
				{`SELECT count(*) FROM agent_inputs WHERE input_idempotency_key=$1`, slot.Input.IdempotencyKey},
			} {
				var count int
				require.NoError(t, f.store.pool.QueryRow(f.ctx, check.query, check.arg).Scan(&count))
				require.Zero(t, count)
			}
			record, err := f.store.Integrations().GetIntegrationInbox(f.ctx, testProjectID, receipt.ID)
			require.NoError(t, err)
			var progress map[string]map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(record.Progress, &progress))
			require.Empty(t, progress["recipient"]["committed"])
		})
	}
}

func TestInboxMessageSiblingsConcurrentAndDelayedFiles(t *testing.T) {
	t.Parallel()
	for _, order := range []string{"concurrent", "files first", "text first"} {
		t.Run(order, func(t *testing.T) {
			t.Parallel()
			f := newAppInteractionFixture(t)
			base := inboxInputPlan(f.process.AgentID, f.app, "text-callback")
			base.Input.Origin.Address = integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:111.222"}
			base.Sibling = &executionstore.InboxMessageSibling{Key: "file-callback"}
			files := withInboxFile(t, base)
			files.Input.IdempotencyKey = "file-callback"
			files.Sibling = &executionstore.InboxMessageSibling{
				Key:              "text-callback",
				AttachmentNotice: "Files for the previous message.",
			}
			textReceipt := freezeInboxInput(t, f.activation(), base, "text-receipt", time.Minute)
			fileReceipt := freezeInboxInput(t, f.activation(), files, "file-receipt", time.Minute)
			prepareInboxInput(t, f.activation(), fileReceipt, files)
			admit := func(r integrationstore.IntegrationInboxRecord) (executionstore.InboxInputResult, error) {
				return f.store.Execution().AdmitInboxInputSlot(f.ctx, r.Lease(), "recipient")
			}
			var laterTool uuid.UUID
			if order == "text first" {
				laterTool = createToolCallForProcessTest(t, f.ctx, f.process, "later-prompt", "ask_question")
			}
			var textResult, fileResult executionstore.InboxInputResult
			var err error
			switch order {
			case "concurrent":
				start := make(chan struct{})
				one := integrationdb.RunAsync(
					func() (executionstore.InboxInputResult, error) { <-start; return admit(textReceipt) },
				)
				two := integrationdb.RunAsync(
					func() (executionstore.InboxInputResult, error) { <-start; return admit(fileReceipt) },
				)
				close(start)
				textResult = integrationdb.AwaitSuccess(t, one, "text sibling")
				fileResult = integrationdb.AwaitSuccess(t, two, "file sibling")
			case "files first":
				fileResult, err = admit(fileReceipt)
				require.NoError(t, err)
				textResult, err = admit(textReceipt)
				require.NoError(t, err)
				require.False(t, textResult.Created)
				require.Equal(t, fileResult.AgentInput.ID, textResult.AgentInput.ID)
			case "text first":
				textResult, err = admit(textReceipt)
				require.NoError(t, err)
				prompt := createQuestionInteractionForTest(t, f.ctx, f.process, laterTool)
				fileResult, err = admit(fileReceipt)
				require.NoError(t, err)
				require.Empty(t, fileResult.CanceledInteractionIDs)
				require.Equal(t, executionstore.AgentInteractionStateOpen, f.read(t, prompt.ID).State)
				require.Contains(t, string(fileResult.ContentBlocks), "Files for the previous message.")
				require.NotContains(t, string(fileResult.ContentBlocks), "Review attached")
			}
			require.True(t, fileResult.Created)
			var artifacts int
			require.NoError(
				t,
				f.store.pool.QueryRow(f.ctx, `SELECT count(*) FROM artifacts WHERE agent_id=$1`, f.process.AgentID).
					Scan(&artifacts),
			)
			require.Equal(t, 1, artifacts)
			// Committed replay follows the recorded winning key even if the plaintext
			// callback was suppressed by a prior file callback.
			replay, err := admit(textReceipt)
			require.NoError(t, err)
			require.Equal(t, textResult.AgentInput.ID, replay.AgentInput.ID)
			require.False(t, replay.Created)
			replay, err = admit(fileReceipt)
			require.NoError(t, err)
			require.Equal(t, fileResult.AgentInput.ID, replay.AgentInput.ID)
			require.False(t, replay.Created)
		})
	}
}
