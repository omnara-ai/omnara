//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

type appInteractionFixture struct {
	ctx           context.Context //nolint:containedctx // Test-lifetime fixture.
	store         *Store
	user          identitystore.UserRecord
	profile       executionstore.AgentProfileRecord
	app, otherApp integrationstore.ProjectAppRecord
	process       processDaemonFixture
	handlers      map[string]agentconfig.AppCapabilityCompiled
	a, b          integrationstore.IntegrationTargetRecord
}

func newAppInteractionFixture(t *testing.T) appInteractionFixture {
	t.Helper()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	user := createIntegrationProjectAdmin(t, ctx, store, "app-interaction@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, "app-interaction")
	f := appInteractionFixture{ctx: ctx, store: store, user: user, profile: profile}
	f.app = f.createApp(t, "chat")
	f.otherApp = f.createApp(t, "other")
	f.process = newProcessDaemonFixtureInStore(
		t,
		f.ctx,
		f.store,
		f.user.ID,
		"app-interaction",
		time.Now().UTC(),
	)
	f.handlers = map[string]agentconfig.AppCapabilityCompiled{
		"chat": {
			AppID: f.app.ID,
		},
		"other": {
			AppID: f.otherApp.ID,
		},
	}
	f.change(t, f.handlers)
	f.a = f.target(t, f.process.AgentID, "C123:111.222")
	f.b = f.target(t, f.process.AgentID, "C456:333.444")
	return f
}

func (f appInteractionFixture) target(
	t *testing.T, agentID uuid.UUID, address string,
) integrationstore.IntegrationTargetRecord {
	t.Helper()
	// Admitting input here would disturb the fixture's already-active tool turn.
	appID := f.app.ID
	if strings.HasPrefix(address, "C456:") {
		appID = f.otherApp.ID
	}
	tx := integrationdb.BeginTx(t, f.ctx, f.store.pool)
	require.NoError(t, lifecyclelock.EnterActiveProject(f.ctx, tx, testOrgID, testProjectID))
	require.NoError(t, integrationstore.LockAppsTx(f.ctx, tx, testProjectID, nil, appID))
	conversation := integrationstore.ConversationAddress{Kind: "thread", Ref: address}
	require.NoError(
		t,
		integrationstore.LockConversationTx(f.ctx, tx, testProjectID, appID, conversation),
	)
	require.NoError(
		t,
		lifecyclelock.Agents(
			f.ctx,
			tx,
			[]lifecyclelock.AgentRef{{ProjectID: testProjectID, AgentID: agentID}},
		),
	)
	target, err := f.store.Integrations().
		EnsureConversationTargetTx(f.ctx, tx, integrationstore.EnsureConversationTargetInput{
			ProjectID: testProjectID, AgentID: agentID, AppID: appID,
			Address: conversation,
		})
	require.NoError(t, err)
	require.NoError(t, tx.Commit(f.ctx))
	return target
}

func (f appInteractionFixture) createApp(
	t *testing.T,
	name string,
) integrationstore.ProjectAppRecord {
	t.Helper()
	credential := createIntegrationCredential(t, f.ctx, f.store, testProjectID, f.user.ID, name)
	secret, err := f.store.Secrets().GetSecret(f.ctx, testOrgID, credential)
	require.NoError(t, err)
	app, err := f.store.Integrations().CreateProjectApp(f.ctx, integrationstore.SaveProjectAppInput{
		OrgID: testOrgID, ProjectID: testProjectID, Name: name, AppType: appdefinition.SlackThread,
	})
	require.NoError(t, err)
	app, err = f.store.Integrations().
		ConfigureProjectApp(f.ctx, integrationstore.ConfigureProjectAppInput{
			OrgID:                 testOrgID,
			ProjectID:             testProjectID,
			AppID:                 app.ID,
			InstalledByUserID:     f.user.ID,
			Provider:              "slack",
			ProviderTenantID:      "T_INTERACTION",
			ProviderAccountRef:    "A_INTERACTION",
			CredentialSecretID:    credential,
			CredentialVersionID:   secret.CurrentVersionID,
			ExpectedSetupRevision: app.SetupRevision,
			OAuthFlowID:           uuid.Must(uuid.NewV7()),
			ProviderIdentity:      json.RawMessage(`{"bot_user_id":"B_INTERACTION"}`),
		})
	require.NoError(t, err)
	return app
}

func (f appInteractionFixture) definition(
	t *testing.T,
	instruction string,
	handlers map[string]agentconfig.AppCapabilityCompiled,
) executionstore.CreateAgentConfigInput {
	t.Helper()
	var compiled agentconfig.Compiled
	require.NoError(t, json.Unmarshal(f.profile.CurrentConfig.CompiledDefinition, &compiled))
	compiled.Instruction, compiled.InteractionHandlers = instruction, handlers
	if compiled.Tools == nil {
		compiled.Tools = map[string]agentconfig.ToolCompiled{}
	}
	for _, name := range toolcatalog.InteractionHandlerToolNames() {
		compiled.Tools[name] = agentconfig.ToolCompiled{
			Enabled:    true,
			Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow),
		}
	}
	encoded, err := agentconfig.EncodeCompiled(compiled)
	require.NoError(t, err)
	return executionstore.CreateAgentConfigInput{
		ProjectID:               testProjectID,
		ConfiguredModelID:       f.profile.CurrentConfig.ConfiguredModelID,
		CompiledDefinition:      encoded.CanonicalJSON,
		EffectiveDefinitionHash: encoded.Hash,
	}
}

func (f appInteractionFixture) change(
	t *testing.T,
	handlers map[string]agentconfig.AppCapabilityCompiled,
) {
	t.Helper()
	key := uuid.NewString()
	_, err := f.store.Execution().ChangeAgentConfig(f.ctx, executionstore.ChangeAgentConfigInput{
		CreateAgentConfigInput: f.definition(t, key, handlers), AgentID: f.process.AgentID,
		ActorType: identitystore.PrincipalTypeUser, ActorID: f.user.ID, IdempotencyKey: key,
	})
	require.NoError(t, err)
}

func (f appInteractionFixture) disable(t *testing.T) {
	t.Helper()
	changed, err := f.store.Integrations().
		DisconnectProjectApp(f.ctx, integrationstore.DisconnectProjectAppInput{ProjectID: testProjectID, AppID: f.app.ID})
	require.NoError(t, err)
	require.True(t, changed)
}

func (f appInteractionFixture) selectOrigin(
	t *testing.T,
	targetID uuid.UUID,
) executionstore.InteractionSelection {
	t.Helper()
	tx := integrationdb.BeginTx(t, f.ctx, f.store.pool)
	_, err := dbsqlc.New(tx).LockAgentInProject(f.ctx, dbsqlc.LockAgentInProjectParams{
		ProjectID: testProjectID, ID: f.process.AgentID,
	})
	require.NoError(t, err)
	selection, err := f.store.Execution().SelectInteractionDestinationForOriginTx(
		f.ctx, tx, testProjectID, f.process.AgentID, targetID,
	)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(f.ctx))
	stored, err := f.store.Execution().
		GetInteractionSelection(f.ctx, testProjectID, f.process.AgentID)
	require.NoError(t, err)
	require.Equal(t, selection.HandlerKey, stored.HandlerKey)
	require.Equal(t, selection.IntegrationTargetID, stored.IntegrationTargetID)
	if selection.HandlerKey != "" {
		require.JSONEq(t, string(selection.Args), string(stored.Args))
	}
	return stored
}

func (f appInteractionFixture) question(t *testing.T) executionstore.AgentInteractionRecord {
	t.Helper()
	id := createToolCallForProcessTest(t, f.ctx, f.process, uuid.NewString(), "ask_question")
	return createQuestionInteractionForTest(t, f.ctx, f.process, id)
}

func (f appInteractionFixture) read(
	t *testing.T,
	id uuid.UUID,
) executionstore.AgentInteractionRecord {
	t.Helper()
	record, found, err := f.store.Execution().
		GetAgentInteraction(f.ctx, testProjectID, f.process.AgentID, id)
	require.NoError(t, err)
	require.True(t, found)
	return record
}

func (f appInteractionFixture) callback(
	t *testing.T,
	record executionstore.AgentInteractionRecord, user string,
) executionstore.ResolveAgentInteractionFromHandlerInput {
	return executionstore.ResolveAgentInteractionFromHandlerInput{
		ResolveAgentInteractionInput: executionstore.ResolveAgentInteractionInput{
			ProjectID: testProjectID,
			AgentID:   f.process.AgentID,
			ID:        record.ID,
			Resolution: interactionform.Resolution{
				Answers: []interactionform.Answer{{OptionIndices: []int{0}}},
			},
			Actor: mustAppActorParams(t, f.app.ID, user),
		},
		AppID: f.app.ID, AppType: appdefinition.SlackThread,
		Address: integrationstore.ConversationAddress{Kind: "thread", Ref: f.a.ProviderRef},
	}
}

func receiptForAppInteraction(
	t *testing.T, record executionstore.AgentInteractionRecord,
) executionstore.RecordInteractionPresentationReceiptInput {
	t.Helper()
	destination, err := record.CapturedDestination()
	require.NoError(t, err)
	require.NotNil(t, destination)
	return executionstore.RecordInteractionPresentationReceiptInput{
		ProjectID:   record.ProjectID,
		AgentID:     record.AgentID,
		ID:          record.ID,
		Destination: *destination,
		Receipt:     json.RawMessage(`{"channel_id":"C123","message_ts":"444.555"}`),
	}
}

func TestAppInteractionsSelectionAndCapturedQuestion(t *testing.T) {
	t.Parallel()
	f := newAppInteractionFixture(t)
	selection := f.selectOrigin(t, f.a.ID)
	require.Equal(t, "chat", selection.HandlerKey)
	require.Equal(t, selection, f.selectOrigin(t, uuid.Nil), "originless inputs preserve selection")
	question := f.question(t)
	destination, err := question.CapturedDestination()
	require.NoError(t, err)
	require.Equal(t, f.a.ID, destination.IntegrationTargetID)
	require.Equal(t, f.app.ID, destination.AppID)
	require.Equal(t, "chat", destination.HandlerKey)
	f.selectOrigin(t, f.b.ID)
	replayed := createQuestionInteractionForTest(t, f.ctx, f.process, question.ToolCallID)
	require.JSONEq(t, string(question.Destination), string(replayed.Destination),
		"question replay keeps its original snapshot")
	selection, err = f.store.Execution().
		GetInteractionSelection(f.ctx, testProjectID, f.process.AgentID)
	require.NoError(t, err)
	require.Equal(t, f.b.ID, selection.IntegrationTargetID)
	listed, err := f.store.Execution().ListAgentInteractionsForAgent(f.ctx,
		executionstore.ListAgentInteractionsForAgentInput{
			ProjectID: testProjectID,
			AgentID:   f.process.AgentID,
			Limit:     10,
		})
	require.NoError(t, err)
	require.Len(t, listed.Interactions, 1)
	require.JSONEq(t, string(question.Destination), string(listed.Interactions[0].Destination))
	_, err = f.store.Execution().
		GetAgentInteractionForPresentation(f.ctx, testProjectID, f.process.AgentID, question.ID)
	require.NoError(t, err, "changing selection does not revoke the captured conversation")
	resolved, err := f.store.Execution().ResolveAgentInteractionFromHandler(
		f.ctx, f.callback(t, question, "U_DIFFERENT_PARTICIPANT"),
	)
	require.NoError(t, err)
	require.Equal(t, executionstore.AgentInteractionStateResolved, resolved.State)
	_, err = f.store.Execution().
		ResolveAgentInteractionFromHandler(f.ctx, f.callback(t, question, "U_DIFFERENT_PARTICIPANT"))
	require.NoError(t, err, "same participant and response replay is idempotent")
	_, err = f.store.Execution().
		ResolveAgentInteractionFromHandler(f.ctx, f.callback(t, question, "U_ANOTHER_PARTICIPANT"))
	require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
	afterResponse, err := f.store.Execution().
		GetInteractionSelection(f.ctx, testProjectID, f.process.AgentID)
	require.NoError(t, err)
	require.Equal(
		t,
		selection,
		afterResponse,
		"an answer to an older prompt does not change current selection",
	)
}

func TestAppInteractionsOriginAmbiguityAndExplicitChoice(t *testing.T) {
	t.Parallel()
	f := newAppInteractionFixture(t)
	f.selectOrigin(t, f.a.ID)
	f.handlers["overlap"] = f.handlers["chat"]
	f.change(t, f.handlers)
	require.Equal(t, executionstore.InteractionSelection{}, f.selectOrigin(t, f.a.ID),
		"ambiguous origin clears the selection")
	toolID := createToolCallForProcessTest(
		t,
		f.ctx,
		f.process,
		"select-destination",
		"set_interaction_handler",
	)
	input := executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       f.process.AgentID,
		ToolCallID:    toolID,
		RuntimeLockID: f.process.Lock.ID,
	}
	selectCommand := func(key string) executionstore.ToolCallPlan {
		return func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
			return executionstore.SetInteractionHandlerForToolCall(
				executionstore.InteractionSelection{
					HandlerKey: key,
					Args:       json.RawMessage(`{"channel_id":"C123","thread_ts":"111.222"}`),
				},
				executionstore.ToolCallCompletionInput{
					Outcome:            executionstore.ToolResultOutcomeSucceeded,
					ResultContentParts: json.RawMessage(`[{"type":"text","text":"selected"}]`),
				},
			), nil
		}
	}
	_, err := f.store.Execution().ExecuteToolCall(f.ctx, input, selectCommand("missing"))
	require.ErrorIs(t, err, storeerr.ErrUnauthorized)
	_, err = f.store.Execution().ExecuteToolCall(f.ctx, input, selectCommand("overlap"))
	require.NoError(t, err)
	current, err := f.store.Execution().
		GetInteractionSelection(f.ctx, testProjectID, f.process.AgentID)
	require.NoError(t, err)
	require.Equal(t, "overlap", current.HandlerKey)
	require.Equal(t, current, f.selectOrigin(t, uuid.Nil))
	unsupported := f.target(t, f.process.AgentID, "C789:555.666")
	require.Equal(t, executionstore.InteractionSelection{}, f.selectOrigin(t, unsupported.ID))
	require.Empty(
		t,
		f.question(t).Destination,
		"unsupported origin does not fall back to a prior handler",
	)
}

func TestAppInteractionsRevocationPreservesDashboardAndSnapshot(t *testing.T) {
	t.Parallel()
	for _, revoke := range []string{"handler removed", "app disconnected"} {
		t.Run(revoke, func(t *testing.T) {
			t.Parallel()
			f := newAppInteractionFixture(t)
			f.selectOrigin(t, f.a.ID)
			question := f.question(t)
			switch revoke {
			case "handler removed":
				delete(f.handlers, "chat")
				f.change(t, f.handlers)
			case "app disconnected":
				f.disable(t)
			}
			tx := integrationdb.BeginTx(t, f.ctx, f.store.pool)
			_, err := dbsqlc.New(tx).LockAgentInProject(f.ctx, dbsqlc.LockAgentInProjectParams{
				ProjectID: testProjectID, ID: f.process.AgentID,
			})
			require.NoError(t, err)
			selection, err := f.store.Execution().ReconcileInteractionSelectionTx(
				f.ctx, tx, testProjectID, f.process.AgentID,
			)
			require.NoError(t, err)
			require.Equal(t, executionstore.InteractionSelection{}, selection)
			require.NoError(t, tx.Commit(f.ctx))
			_, err = f.store.Execution().GetAgentInteractionForPresentation(
				f.ctx, testProjectID, f.process.AgentID, question.ID,
			)
			require.ErrorIs(t, err, storeerr.ErrUnauthorized)
			_, err = f.store.Execution().
				ResolveAgentInteractionFromHandler(f.ctx, f.callback(t, question, "U_OTHER"))
			require.ErrorIs(t, err, storeerr.ErrUnauthorized)
			unchanged := f.read(t, question.ID)
			require.Equal(t, executionstore.AgentInteractionStateOpen, unchanged.State)
			require.JSONEq(t, string(question.Destination), string(unchanged.Destination))
			input := f.callback(t, question, "U_OTHER").ResolveAgentInteractionInput
			input.Actor = mustOmnaraActorParams(t, f.user.ID)
			resolved, err := f.store.Execution().ResolveAgentInteraction(f.ctx, input)
			require.NoError(t, err, "dashboard remains authoritative when mirroring is unavailable")
			require.Equal(t, executionstore.AgentInteractionStateResolved, resolved.State)
		})
	}
}

func TestAppInteractionsRejectForeignCallbackAndTarget(t *testing.T) {
	t.Parallel()
	f := newAppInteractionFixture(t)
	f.selectOrigin(t, f.a.ID)
	question := f.question(t)
	for _, change := range []func(*executionstore.ResolveAgentInteractionFromHandlerInput){
		func(v *executionstore.ResolveAgentInteractionFromHandlerInput) { v.AppID = uuid.New() },
		func(v *executionstore.ResolveAgentInteractionFromHandlerInput) { v.Address.Ref = f.b.ProviderRef },
		func(v *executionstore.ResolveAgentInteractionFromHandlerInput) {
			v.AppType = appdefinition.DiscordThread
		},
		func(v *executionstore.ResolveAgentInteractionFromHandlerInput) { v.IntegrationTargetID = f.b.ID },
		func(v *executionstore.ResolveAgentInteractionFromHandlerInput) { v.Actor = nil },
		func(v *executionstore.ResolveAgentInteractionFromHandlerInput) {
			v.Actor.ProviderTenantID = "OTHER_APP"
		},
		func(v *executionstore.ResolveAgentInteractionFromHandlerInput) {
			v.Actor.Provider = executionstore.ActorProviderExternal
		},
	} {
		input := f.callback(t, question, "U_OTHER")
		change(&input)
		_, err := f.store.Execution().ResolveAgentInteractionFromHandler(f.ctx, input)
		require.ErrorIs(t, err, storeerr.ErrUnauthorized)
	}
	otherAgent := mustCreateAgent(t, f.ctx, f.store)
	foreign := f.target(t, otherAgent, "C123:111.222")
	tx := integrationdb.BeginTx(t, f.ctx, f.store.pool)
	_, err := dbsqlc.New(tx).LockAgentInProject(f.ctx, dbsqlc.LockAgentInProjectParams{
		ProjectID: testProjectID, ID: f.process.AgentID,
	})
	require.NoError(t, err)
	_, err = f.store.Execution().SelectInteractionDestinationForOriginTx(
		f.ctx, tx, testProjectID, f.process.AgentID, foreign.ID,
	)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	require.NoError(t, tx.Rollback(f.ctx))
	_, err = f.store.Execution().
		GetAgentInteractionForPresentation(f.ctx, uuid.New(), f.process.AgentID, question.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	require.Equal(t, executionstore.AgentInteractionStateOpen, f.read(t, question.ID).State)
}

func TestAppInteractionsIdentityCannotRedirectCapturedPrompt(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"handler key reused", "target address changed"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			f := newAppInteractionFixture(t)
			f.selectOrigin(t, f.a.ID)
			question := f.question(t)
			if change == "handler key reused" {
				replacement := f.createApp(t, "replacement")
				handler := f.handlers["chat"]
				handler.AppID = replacement.ID
				f.handlers["chat"] = handler
				f.change(t, f.handlers)
			} else {
				_, err := f.store.pool.Exec(
					f.ctx,
					`UPDATE integration_targets SET provider_ref='C123:999.888' WHERE project_id=$1 AND agent_id=$2 AND id=$3`,
					testProjectID,
					f.process.AgentID,
					f.a.ID,
				)
				require.NoError(t, err)
			}
			_, err := f.store.Execution().GetAgentInteractionForPresentation(
				f.ctx, testProjectID, f.process.AgentID, question.ID,
			)
			require.ErrorIs(t, err, storeerr.ErrUnauthorized)
			_, err = f.store.Execution().
				ResolveAgentInteractionFromHandler(f.ctx, f.callback(t, question, "U_OTHER"))
			require.ErrorIs(t, err, storeerr.ErrUnauthorized)
			require.JSONEq(
				t,
				string(question.Destination),
				string(f.read(t, question.ID).Destination),
			)
		})
	}
}

func TestAppInteractionsCaptureUsesLockedCurrentSelectionWithoutAppGate(t *testing.T) {
	t.Parallel()
	f := newAppInteractionFixture(t)
	f.selectOrigin(t, f.a.ID)
	toolID := createToolCallForProcessTestWithPermission(
		t,
		f.ctx,
		f.process,
		"captured-permission",
		"run_command",
		false,
	)
	request, err := toolpermission.ParseRequest(permissionRequestForStorageTest(t, "run_command"))
	require.NoError(t, err)
	appGate := integrationdb.BeginTx(t, f.ctx, f.store.pool)
	require.NoError(t, dbsqlc.New(appGate).LockProjectAppLifecycleExclusive(f.ctx,
		dbsqlc.LockProjectAppLifecycleExclusiveParams{AppID: f.app.ID}))
	selectionTx := integrationdb.BeginTx(t, f.ctx, f.store.pool)
	_, err = dbsqlc.New(selectionTx).LockAgentInProject(f.ctx, dbsqlc.LockAgentInProjectParams{
		ProjectID: testProjectID, ID: f.process.AgentID,
	})
	require.NoError(t, err)
	done := integrationdb.RunAsync(func() (executionstore.AgentInteractionRecord, error) {
		return f.store.Execution().
			CreatePermissionInteraction(f.ctx, executionstore.CreatePermissionInteractionInput{
				ProjectID: testProjectID, AgentID: f.process.AgentID, ToolCallID: toolID,
				RuntimeLockID: f.process.Lock.ID, Request: request,
			})
	})
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.store.pool, "LockAgentInProject", 1)
	_, err = f.store.Execution().SelectInteractionDestinationForOriginTx(
		f.ctx, selectionTx, testProjectID, f.process.AgentID, f.b.ID,
	)
	require.NoError(t, err)
	require.NoError(t, selectionTx.Commit(f.ctx))
	interaction := integrationdb.AwaitSuccess(
		t,
		done,
		"capture without acquiring a app gate after the agent lock",
	)
	destination, err := interaction.CapturedDestination()
	require.NoError(t, err)
	require.Equal(t, f.b.ID, destination.IntegrationTargetID)
	require.Equal(t, "other", destination.HandlerKey)
	require.NoError(t, appGate.Rollback(f.ctx))
	callback := f.callback(t, interaction, "U_OTHER_PARTICIPANT")
	callback.Address.Ref = f.b.ProviderRef
	callback.AppID = f.otherApp.ID
	callback.Actor = mustAppActorParams(t, f.otherApp.ID, "U_OTHER_PARTICIPANT")
	callback.Resolution.Answers[0].OptionIndices = []int{toolpermission.AllowOptionIndex}
	_, err = f.store.Execution().ResolveAgentInteractionFromHandler(f.ctx, callback)
	require.NoError(t, err)
	var provider, tenant, sender string
	require.NoError(t, f.store.pool.QueryRow(f.ctx, `
SELECT actor.provider, actor.provider_tenant_id, actor.provider_user_id
FROM agent_interactions interaction
JOIN agent_inputs input ON input.id=interaction.resolved_by_input_id
JOIN actors actor ON actor.id=input.actor_id
WHERE interaction.id=$1`, interaction.ID).Scan(&provider, &tenant, &sender))
	require.Equal(t, executionstore.ActorProviderApp, provider)
	require.Equal(t, callback.Actor.ProviderTenantID, tenant)
	require.Equal(t, "U_OTHER_PARTICIPANT", sender)
	tool, err := f.store.Execution().GetToolCall(f.ctx, testProjectID, f.process.AgentID, toolID)
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolCallStateReady, tool.State)
}

func TestAppInteractionsCallbackFencesRevocationBeforeAgentLock(t *testing.T) {
	t.Parallel()
	f := newAppInteractionFixture(t)
	f.selectOrigin(t, f.a.ID)
	question := f.question(t)
	revocation := integrationdb.BeginTx(t, f.ctx, f.store.pool)
	q := dbsqlc.New(revocation)
	require.NoError(t, q.LockProjectAppLifecycleExclusive(f.ctx,
		dbsqlc.LockProjectAppLifecycleExclusiveParams{AppID: f.app.ID}))
	doneInput := f.callback(t, question, "U_OTHER")
	done := integrationdb.RunAsync(func() (executionstore.AgentInteractionRecord, error) {
		return f.store.Execution().
			ResolveAgentInteractionFromHandler(f.ctx, doneInput)
	})
	integrationdb.WaitForNamedLockWaiters(
		t,
		f.ctx,
		f.store.pool,
		"LockProjectAppLifecycleShared",
		1,
	)
	lockCtx, cancel := context.WithTimeout(f.ctx, 2*time.Second)
	defer cancel()
	_, err := q.LockAgentInProject(lockCtx, dbsqlc.LockAgentInProjectParams{
		ProjectID: testProjectID, ID: f.process.AgentID,
	})
	require.NoError(t, err, "callback must not hold the agent while waiting for its app")
	_, err = revocation.Exec(
		f.ctx,
		`UPDATE project_apps SET state='disconnected' WHERE project_id=$1 AND id=$2`,
		testProjectID,
		f.app.ID,
	)
	require.NoError(t, err)
	require.NoError(t, revocation.Commit(f.ctx))
	result := integrationdb.Await(t, done, "callback after app revocation")
	require.ErrorIs(t, result.Err, storeerr.ErrUnauthorized)
	require.Equal(t, executionstore.AgentInteractionStateOpen, f.read(t, question.ID).State)
}

func TestAppInteractionsCallbackFencesVerifiedSetupRevision(t *testing.T) {
	t.Parallel()
	f := newAppInteractionFixture(t)
	f.selectOrigin(t, f.a.ID)
	question := f.question(t)
	input := f.callback(t, question, "U_OTHER")
	input.SourceSetupRevision = f.app.SetupRevision
	rotation := integrationdb.BeginTx(t, f.ctx, f.store.pool)
	require.NoError(t, dbsqlc.New(rotation).LockProjectAppLifecycleExclusive(f.ctx,
		dbsqlc.LockProjectAppLifecycleExclusiveParams{AppID: f.app.ID}))
	done := integrationdb.RunAsync(func() (executionstore.AgentInteractionRecord, error) {
		return f.store.Execution().ResolveAgentInteractionFromHandler(f.ctx, input)
	})
	integrationdb.WaitForNamedLockWaiters(
		t,
		f.ctx,
		f.store.pool,
		"LockProjectAppLifecycleShared",
		1,
	)
	_, err := rotation.Exec(
		f.ctx,
		`UPDATE project_apps SET setup_revision=setup_revision+1 WHERE project_id=$1 AND id=$2`,
		testProjectID,
		f.app.ID,
	)
	require.NoError(t, err)
	require.NoError(t, rotation.Commit(f.ctx))
	result := integrationdb.Await(t, done, "callback verified before app setup revision changed")
	require.ErrorIs(t, result.Err, storeerr.ErrUnauthorized)
	require.Equal(t, executionstore.AgentInteractionStateOpen, f.read(t, question.ID).State)
	current, err := f.store.Integrations().GetProjectApp(f.ctx, testProjectID, f.app.ID)
	require.NoError(t, err)
	input.SourceSetupRevision = current.SetupRevision
	resolved, err := f.store.Execution().ResolveAgentInteractionFromHandler(f.ctx, input)
	require.NoError(t, err)
	require.Equal(t, executionstore.AgentInteractionStateResolved, resolved.State)
}

func TestAppInteractionsReceiptCancelRaceAndLateConfirmation(t *testing.T) {
	t.Parallel()
	for _, late := range []bool{false, true} {
		t.Run(fmt.Sprintf("late=%v", late), func(t *testing.T) {
			t.Parallel()
			f := newAppInteractionFixture(t)
			f.selectOrigin(t, f.a.ID)
			question := f.question(t)
			input := receiptForAppInteraction(t, question)
			cancelInput := executionstore.CancelAgentInput{
				ProjectID: testProjectID,
				AgentID:   f.process.AgentID,
				Actor:     mustOmnaraActorParams(t, f.user.ID),
			}
			if late {
				_, err := f.store.Execution().CancelAgent(f.ctx, cancelInput)
				require.NoError(t, err)
				f.disable(t)
				_, err = f.store.Execution().RecordInteractionPresentationReceipt(f.ctx, input)
				require.NoError(
					t,
					err,
					"confirmed receipt survives cancellation and app revocation",
				)
			} else {
				barrier := integrationdb.BeginTx(t, f.ctx, f.store.pool)
				_, err := dbsqlc.New(barrier).
					LockAgentInProject(f.ctx, dbsqlc.LockAgentInProjectParams{
						ProjectID: testProjectID, ID: f.process.AgentID,
					})
				require.NoError(t, err)
				receipt := integrationdb.RunAsync(
					func() (executionstore.AgentInteractionRecord, error) {
						return f.store.Execution().
							RecordInteractionPresentationReceipt(f.ctx, input)
					},
				)
				canceled := integrationdb.RunAsync(
					func() (executionstore.CancelAgentResult, error) {
						return f.store.Execution().CancelAgent(f.ctx, cancelInput)
					},
				)
				integrationdb.WaitForNamedLockWaiters(
					t,
					f.ctx,
					f.store.pool,
					"LockAgentInProject",
					2,
				)
				require.NoError(t, barrier.Commit(f.ctx))
				integrationdb.AwaitSuccess(t, receipt, "receipt racing cancellation")
				integrationdb.AwaitSuccess(t, canceled, "cancellation racing receipt")
			}
			record := f.read(t, question.ID)
			require.Equal(t, executionstore.AgentInteractionStateCanceled, record.State)
			require.JSONEq(t, string(question.Destination), string(record.Destination))
			require.JSONEq(t, string(input.Receipt), string(record.PresentationReceipt))
			replayed, err := f.store.Execution().RecordInteractionPresentationReceipt(f.ctx, input)
			require.NoError(t, err)
			require.Equal(t, record, replayed)
			listed, err := f.store.Execution().ListAgentInteractionsForAgent(f.ctx,
				executionstore.ListAgentInteractionsForAgentInput{
					ProjectID: testProjectID, AgentID: f.process.AgentID, Limit: 10,
				})
			require.NoError(t, err)
			require.Len(t, listed.Interactions, 1)
			require.JSONEq(
				t,
				string(input.Receipt),
				string(listed.Interactions[0].PresentationReceipt),
			)
			input.Receipt = json.RawMessage(`{"message_ts":"different"}`)
			_, err = f.store.Execution().RecordInteractionPresentationReceipt(f.ctx, input)
			require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
			input.Destination.AppID = uuid.New()
			_, err = f.store.Execution().RecordInteractionPresentationReceipt(f.ctx, input)
			require.ErrorIs(t, err, storeerr.ErrUnauthorized)
		})
	}
}

func TestAppInteractionsConcurrentParticipantsResolveOnce(t *testing.T) {
	t.Parallel()
	f := newAppInteractionFixture(t)
	f.selectOrigin(t, f.a.ID)
	question := f.question(t)
	barrier := integrationdb.BeginTx(t, f.ctx, f.store.pool)
	_, err := dbsqlc.New(barrier).LockAgentInProject(f.ctx, dbsqlc.LockAgentInProjectParams{
		ProjectID: testProjectID, ID: f.process.AgentID,
	})
	require.NoError(t, err)
	firstInput := f.callback(t, question, "U_FIRST")
	first := integrationdb.RunAsync(func() (executionstore.AgentInteractionRecord, error) {
		return f.store.Execution().
			ResolveAgentInteractionFromHandler(f.ctx, firstInput)
	})
	secondInput := f.callback(t, question, "U_SECOND")
	second := integrationdb.RunAsync(func() (executionstore.AgentInteractionRecord, error) {
		return f.store.Execution().
			ResolveAgentInteractionFromHandler(f.ctx, secondInput)
	})
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.store.pool, "LockAgentInProject", 2)
	require.NoError(t, barrier.Commit(f.ctx))
	winners := 0
	for _, result := range []integrationdb.AsyncResult[executionstore.AgentInteractionRecord]{
		integrationdb.Await(t, first, "first participant"), integrationdb.Await(t, second, "second participant"),
	} {
		if result.Err == nil {
			winners++
		} else {
			require.ErrorIs(t, result.Err, storeerr.ErrIdempotencyConflict)
		}
	}
	require.Equal(t, 1, winners)
	var responses int
	require.NoError(t, f.store.pool.QueryRow(
		f.ctx,
		`SELECT count(*) FROM agent_inputs WHERE project_id=$1 AND agent_id=$2 AND target_interaction_id=$3`,
		testProjectID,
		f.process.AgentID,
		question.ID,
	).Scan(&responses))
	require.Equal(t, 1, responses)
	record := f.read(t, question.ID)
	require.Equal(t, executionstore.AgentInteractionStateResolved, record.State)
	_, err = f.store.Execution().
		RecordInteractionPresentationReceipt(f.ctx, receiptForAppInteraction(t, record))
	require.NoError(t, err, "a confirmed send may arrive after either terminal state")
}

func TestAppInteractionsCallbackRechecksConfigAfterAgentLockWait(t *testing.T) {
	t.Parallel()
	f := newAppInteractionFixture(t)
	f.selectOrigin(t, f.a.ID)
	question := f.question(t)
	definition := f.definition(t, "handler revoked while callback waits", nil)
	config, err := f.store.Execution().CreateAgentConfig(f.ctx, definition)
	require.NoError(t, err)
	activation := integrationdb.BeginTx(t, f.ctx, f.store.pool)
	_, err = dbsqlc.New(activation).LockAgentInProject(f.ctx, dbsqlc.LockAgentInProjectParams{
		ProjectID: testProjectID, ID: f.process.AgentID,
	})
	require.NoError(t, err)
	doneInput := f.callback(t, question, "U_OTHER")
	done := integrationdb.RunAsync(func() (executionstore.AgentInteractionRecord, error) {
		return f.store.Execution().
			ResolveAgentInteractionFromHandler(f.ctx, doneInput)
	})
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.store.pool, "LockAgentInProject", 1)
	_, err = activation.Exec(
		f.ctx,
		`UPDATE agents SET current_config_id=$3 WHERE project_id=$1 AND id=$2`,
		testProjectID,
		f.process.AgentID,
		config.ID,
	)
	require.NoError(t, err)
	_, err = f.store.Execution().
		ReconcileInteractionSelectionTx(f.ctx, activation, testProjectID, f.process.AgentID)
	require.NoError(t, err)
	require.NoError(t, activation.Commit(f.ctx))
	result := integrationdb.Await(t, done, "callback after config activation")
	require.ErrorIs(t, result.Err, storeerr.ErrUnauthorized)
	require.Equal(t, executionstore.AgentInteractionStateOpen, f.read(t, question.ID).State)
}

func TestAppInteractionsDatabaseKeepsSnapshotAndLifecycleImmutable(t *testing.T) {
	t.Parallel()
	f := newAppInteractionFixture(t)
	f.selectOrigin(t, f.a.ID)
	question := f.question(t)
	for _, update := range []string{
		`destination = '{}'`,
		`destination = NULL`,
		`presentation_receipt = '{}', request = '{}'`,
		`presentation_receipt = '{}', state = 'canceled', resolved_at = statement_timestamp()`,
	} {
		_, err := f.store.pool.Exec(
			f.ctx,
			`UPDATE agent_interactions SET `+update+` WHERE agent_id=$1 AND id=$2`,
			f.process.AgentID,
			question.ID,
		)
		require.True(t, isPgReadOnlySQLTransaction(err), "update %s: %v", update, err)
	}
	input := receiptForAppInteraction(t, question)
	for _, receipt := range []string{
		`[]`, `{"huge":"` + strings.Repeat("x", executionstore.InteractionReceiptMaxBytes) + `"}`,
	} {
		_, err := f.store.pool.Exec(f.ctx,
			`UPDATE agent_interactions SET presentation_receipt=$3 WHERE agent_id=$1 AND id=$2`,
			f.process.AgentID, question.ID, receipt,
		)
		require.True(t, isPgCheckViolation(err), "receipt check: %v", err)
	}
	_, err := f.store.Execution().RecordInteractionPresentationReceipt(f.ctx, input)
	require.NoError(t, err)
	_, err = f.store.pool.Exec(f.ctx,
		`UPDATE agent_interactions SET presentation_receipt=NULL WHERE agent_id=$1 AND id=$2`,
		f.process.AgentID, question.ID,
	)
	require.True(t, isPgReadOnlySQLTransaction(err))
	for _, test := range []struct {
		destination string
		receipt     any
	}{
		{`[]`, nil},
		{`{"huge":"` + strings.Repeat("x", executionstore.InteractionDestinationMaxBytes) + `"}`, nil},
		{string(question.Destination), `{}`},
	} {
		_, err := f.store.pool.Exec(
			f.ctx,
			`INSERT INTO agent_interactions(agent_id,tool_call_id,interaction_kind,state,request,created_at,destination,presentation_receipt) VALUES($1,$2,'permission','open','{}',statement_timestamp(),$3,$4)`,
			f.process.AgentID,
			question.ToolCallID,
			test.destination,
			test.receipt,
		)
		require.True(t, isPgCheckViolation(err), "insert destination=%d receipt=%v: %v",
			len(test.destination), test.receipt, err)
	}
	input.Receipt = json.RawMessage(
		`{"huge":"` + strings.Repeat("x", executionstore.InteractionReceiptMaxBytes) + `"}`,
	)
	_, err = f.store.Execution().RecordInteractionPresentationReceipt(f.ctx, input)
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
}

func TestInteractionSelectionDatabaseRequiresCompleteSelection(t *testing.T) {
	t.Parallel()
	f := newAppInteractionFixture(t)
	for _, fields := range []string{
		"integration_target_id=$2",
		"interaction_handler_key='chat'",
		"interaction_handler_args='{}'::jsonb",
		"integration_target_id=$2, interaction_handler_key='chat'",
		"interaction_handler_key='chat', interaction_handler_args='{}'::jsonb",
		"integration_target_id=$2, interaction_handler_args='{}'::jsonb",
		"integration_target_id=$2, interaction_handler_key='', interaction_handler_args='{}'::jsonb",
		"integration_target_id=$2, interaction_handler_key='chat', interaction_handler_args='null'::jsonb",
	} {
		args := []any{f.process.AgentID}
		if strings.Contains(fields, "$2") {
			args = append(args, f.a.ID)
		}
		_, err := f.store.pool.Exec(f.ctx, "UPDATE agents SET "+fields+" WHERE id=$1", args...)
		var constraint *pgconn.PgError
		require.ErrorAs(t, err, &constraint, fields)
		require.Equal(t, "23514", constraint.Code, fields)
	}
	selection, err := f.store.Execution().
		GetInteractionSelection(f.ctx, testProjectID, f.process.AgentID)
	require.NoError(t, err)
	require.Equal(t, executionstore.InteractionSelection{}, selection)
}
