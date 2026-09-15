//go:build integration

package executionstore_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

type externalRequestFixture struct {
	processDaemonFixture
	call       ToolCallRecord
	target     integrationstore.IntegrationTargetRecord
	definition integrationstore.ChannelDefinition
	binding    integrationstore.IntegrationTargetBindingRecord
	input      executionstore.CreateExternalChannelRequestInput
}

func newExternalRequestFixture(t *testing.T, ctx context.Context, toolName string) externalRequestFixture {
	t.Helper()
	f, _, claim := newStartedNormalModelCallTestFixture(t, ctx, "external-request")
	admin := createIntegrationProjectAdmin(t, ctx, f.Store, "external-request@example.com")
	install, err := f.Store.Integrations().CreateExternalIntegrationInstall(ctx, externalConnectionInput(admin.ID))
	require.NoError(t, err)
	definitionInput := externalDefinitionInput(install.ID)
	definitionInput.Capabilities.Questions, definitionInput.Capabilities.Permissions = true, true
	definitionInput.SendParamsSchema = json.RawMessage(`{"type":"object"}`)
	definition, err := f.Store.Integrations().PublishExternalChannelDefinition(ctx, definitionInput)
	require.NoError(t, err)
	target, err := f.Store.Integrations().CreateIntegrationTarget(ctx, integrationstore.CreateIntegrationTargetInput{
		ProjectID: testProjectID, IntegrationInstallID: install.ID, ChannelDefinitionID: definition.ID,
		ProviderRef: "external-root", ProviderRefKind: "conversation",
	})
	require.NoError(t, err)
	binding, err := f.Store.Integrations().CreateIntegrationTargetBinding(
		ctx, integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: testProjectID, AgentID: f.AgentID, IntegrationInstallID: install.ID, IntegrationTargetID: target.ID,
			ReadAllowed: true, SendAllowed: true, ReceiveAllowed: true, Source: "external-request-test",
			ReplyChannelGrants: &integrationstore.ChannelGrants{ReceiveAllowed: true, SendAllowed: true},
		})
	require.NoError(t, err)
	channelID, err := publicid.Encode(publicid.KindIntegrationTarget, target.ID)
	require.NoError(t, err)
	args := json.RawMessage(`{"channel_id":"` + channelID + `","message":{"text":"hello"},` +
		`"params":{"large":9007199254740993,"omnara_channel":"business"}}`)
	if toolName == toolcatalog.ToolNameReadChannel {
		args = json.RawMessage(`{"channel_id":"` + channelID + `","limit":3}`)
	}
	if toolName == toolcatalog.ToolNameAskQuestion {
		args, err = json.Marshal(questionInteractionFormForTest(t))
		require.NoError(t, err)
	}
	_, calls := recordToolCallBatchForContextTest(t, ctx, f, claim.Context.ID, "external-request",
		[]toolCallForContextTest{{
			ProviderCallID: "external", Name: toolName, Input: args, Type: toolcatalog.ToolTypeBuiltIn,
		}},
		f.Now)
	markToolCallReadyForTest(t, ctx, f, calls[0].ID, f.Now)
	payload, err := json.Marshal(channelconnector.SendPayload{
		Destination: channelconnector.OperationDestination{
			ImplementationKey: definition.ImplementationKey, ProviderRef: target.ProviderRef,
			ProviderRefKind: target.ProviderRefKind, ProviderMetadata: target.ProviderMetadata,
		},
		Message: channelconnector.Message{Text: "hello"},
		Params:  json.RawMessage(`{"large":9007199254740993,"omnara_channel":"business"}`),
	})
	require.NoError(t, err)
	operation, createsReplyChannel := channelconnector.OperationSend, true
	if toolName == toolcatalog.ToolNameReadChannel {
		operation, createsReplyChannel = channelconnector.OperationRead, false
		payload, err = json.Marshal(channelconnector.ReadPayload{
			Destination: channelconnector.OperationDestination{
				ImplementationKey: definition.ImplementationKey, ProviderRef: target.ProviderRef,
				ProviderRefKind: target.ProviderRefKind, ProviderMetadata: target.ProviderMetadata,
			},
			Limit: 3,
		})
		require.NoError(t, err)
	}
	return externalRequestFixture{
		processDaemonFixture: f, call: calls[0], target: target, definition: definition, binding: binding,
		input: executionstore.CreateExternalChannelRequestInput{
			TurnID: calls[0].TurnID, ChannelID: target.ID, Operation: operation,
			Payload: payload, CreatesReplyChannel: createsReplyChannel,
		}}
}

func (f externalRequestFixture) start(
	t *testing.T, ctx context.Context, lifetime time.Duration,
) executionstore.ExternalChannelRequestRecord {
	t.Helper()
	input := f.input
	input.Timeout = lifetime
	result, err := f.Store.Execution().ExecuteToolCall(ctx, executionstore.ExecuteToolCallInput{
		ProjectID: testProjectID, AgentID: f.AgentID, ToolCallID: f.call.ID, RuntimeLockID: f.Lock.ID,
	}, func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
		return executionstore.CreateExternalChannelRequestForToolCall(input), nil
	})
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolCallDispositionWaiting, result.Disposition)
	request, ok := result.CommandResult.(executionstore.ExternalChannelRequestRecord)
	require.True(t, ok)
	return request
}

func externalRequestCompletion(
	t *testing.T, request executionstore.ExternalChannelRequestRecord,
) executionstore.CompleteExternalChannelRequestInput {
	t.Helper()
	id, err := publicid.Encode(publicid.KindExternalChannelRequest, request.ID)
	require.NoError(t, err)
	return executionstore.CompleteExternalChannelRequestInput{
		ProjectID: request.ProjectID, IntegrationInstallID: request.IntegrationInstallID, ID: request.ID,
		Result: channelconnector.OperationResult{
			RequestID: id, Outcome: channelconnector.OperationCompleted,
			Payload: json.RawMessage(`{"publication":"published","message_channel":"destination","message_id":"message-1"}`),
		},
	}
}

func TestExternalChannelRequestRestartAndConcurrentCompletionPreserveCanonicalAuthority(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newExternalRequestFixture(t, ctx, toolcatalog.ToolNameSendChannelMessage)
	before, err := f.Store.Execution().GetToolCall(ctx, testProjectID, f.AgentID, f.call.ID)
	require.NoError(t, err)
	request := f.start(t, ctx, time.Minute)
	replay := f.start(t, ctx, 2*time.Minute)
	require.Equal(t, request.ID, replay.ID)
	require.Equal(t, request.Deadline, replay.Deadline, "replay cannot extend the deadline")
	fresh := newIntegrationStore(f.Store.pool).Execution()
	for range 2 {
		page, err := fresh.ListPendingExternalChannelRequests(ctx, executionstore.ListExternalChannelRequestsInput{
			ProjectID: testProjectID, IntegrationInstallID: request.IntegrationInstallID, Limit: 10,
		})
		require.NoError(t, err)
		require.Len(t, page.Requests, 1)
		require.Equal(t, request.ID, page.Requests[0].ID)
	}
	call, err := fresh.GetToolCall(ctx, testProjectID, f.AgentID, f.call.ID)
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolCallStateWaiting, call.State)
	require.Equal(t, NilID, call.RuntimeLockID)
	input := externalRequestCompletion(t, request)
	results := make([]executionstore.CompleteExternalChannelRequestResult, 2)
	errs := make([]error, 2)
	var workers sync.WaitGroup
	for i := range results {
		workers.Go(func() { results[i], errs[i] = fresh.CompleteExternalChannelRequest(ctx, input) })
	}
	workers.Wait()
	for i := range results {
		require.NoError(t, errs[i])
		require.NotNil(t, results[i].ToolCall)
		require.Equal(t, executionstore.ToolResultOutcomeSucceeded, results[i].ToolCall.Outcome)
		require.Equal(t, before.Input, results[i].ToolCall.Input)
	}
	require.NotEqual(t, results[0].Replayed, results[1].Replayed)
	input.Result.Payload = json.RawMessage(`{"publication":"draft","message_channel":"destination"}`)
	_, err = fresh.CompleteExternalChannelRequest(ctx, input)
	require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
	f.requireOneResult(t, ctx)
}

func (f externalRequestFixture) requireOneResult(t *testing.T, ctx context.Context) {
	t.Helper()
	var results, events int
	require.NoError(t, f.Store.pool.QueryRow(ctx,
		`SELECT count(*) FROM tool_call_results WHERE tool_call_id = $1`, f.call.ID).Scan(&results))
	require.NoError(t, f.Store.pool.QueryRow(ctx, `SELECT count(*) FROM agent_events event
		JOIN tool_call_results result ON result.id = event.tool_call_result_id WHERE result.tool_call_id = $1`,
		f.call.ID).Scan(&events))
	require.Equal(t, 1, results)
	require.Equal(t, 1, events)
}

func TestExternalChannelRequestCompletionReplayAfterBindingRevocationDoesNotRegrant(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newExternalRequestFixture(t, ctx, toolcatalog.ToolNameSendChannelMessage)
	request := f.start(t, ctx, time.Minute)
	input := externalRequestCompletion(t, request)
	input.Result.Payload = json.RawMessage(`{"publication":"published","message_channel":"reply_channel",
		"message_id":"message-1","reply_channel":{"implementation_key":"conversation",` +
		`"provider_ref":"thread-1","provider_ref_kind":"thread"}}`)
	completed, err := f.Store.Execution().CompleteExternalChannelRequest(ctx, input)
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolResultOutcomeSucceeded, completed.ToolCall.Outcome)
	var childID, childBindingID ID
	require.NoError(t, f.Store.pool.QueryRow(ctx, `SELECT target.id, binding.id FROM integration_targets target
		JOIN integration_target_bindings binding ON binding.integration_target_id = target.id
		WHERE target.parent_channel_id = $1 AND binding.agent_id = $2`,
		f.target.ID, f.AgentID).Scan(&childID, &childBindingID))
	require.NoError(t, f.Store.Integrations().RevokeIntegrationTargetBinding(ctx, testProjectID, childBindingID))
	require.NoError(t, f.Store.Integrations().RevokeIntegrationTargetBinding(ctx, testProjectID, f.binding.ID))
	replayed, err := newIntegrationStore(f.Store.pool).Execution().CompleteExternalChannelRequest(ctx, input)
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	require.JSONEq(t, string(completed.ToolCall.ResultContentParts), string(replayed.ToolCall.ResultContentParts))
	var activeBindings int
	require.NoError(t, f.Store.pool.QueryRow(ctx, `SELECT count(*) FROM integration_target_bindings
		WHERE integration_target_id = $1 AND revoked_at IS NULL`, childID).Scan(&activeBindings))
	require.Zero(t, activeBindings)
	f.requireOneResult(t, ctx)
}

func TestExternalChannelRequestRecordsPublicationWhenReplyRegistrationFails(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var diagnostics bytes.Buffer
	ctx = log.WithLogger(ctx, slog.New(slog.NewJSONHandler(&diagnostics, nil)))
	f := newExternalRequestFixture(t, ctx, toolcatalog.ToolNameSendChannelMessage)
	request := f.start(t, ctx, time.Minute)
	input := externalRequestCompletion(t, request)
	input.Result.Payload = json.RawMessage(`{"publication":"published","message_channel":"reply_channel",
		"message_id":"published-1","reply_channel":{"implementation_key":"missing_definition",` +
		`"provider_ref":"thread-1","provider_ref_kind":"thread"}}`)
	result, err := f.Store.Execution().CompleteExternalChannelRequest(ctx, input)
	require.NoError(t, err, "known publication must commit even when local registration fails")
	require.Equal(t, executionstore.ExternalChannelRequestCompleted, result.Request.State)
	require.Equal(t, executionstore.ToolResultOutcomeFailed, result.ToolCall.Outcome)
	require.Contains(t, string(result.ToolCall.ResultContentParts), `"publication":"published"`)
	require.Contains(t, string(result.ToolCall.ResultContentParts), "continuation_error")
	require.Contains(t, diagnostics.String(), "channel.reply_registration.failed")
	require.Contains(t, diagnostics.String(), "load reply channel definition: no rows in result set")
	require.NotContains(t, diagnostics.String(), "published-1")
	logged := diagnostics.String()
	replayed, err := f.Store.Execution().CompleteExternalChannelRequest(ctx, input)
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	require.JSONEq(t, string(result.ToolCall.ResultContentParts), string(replayed.ToolCall.ResultContentParts))
	require.Equal(t, logged, diagnostics.String(), "terminal replay does not attempt registration again")
	f.requireOneResult(t, ctx)
}

func TestExternalChannelRequestCancelAndDeadlineAreTerminal(t *testing.T) {
	t.Parallel()
	for _, cancel := range []bool{true, false} {
		name := "deadline"
		if cancel {
			name = "cancel"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			f := newExternalRequestFixture(t, ctx, toolcatalog.ToolNameSendChannelMessage)
			lifetime := time.Second
			if cancel {
				lifetime = time.Minute
			}
			request := f.start(t, ctx, lifetime)
			if cancel {
				_, err := f.Store.Execution().CancelAgent(ctx, executionstore.CancelAgentInput{
					ProjectID: testProjectID, AgentID: f.AgentID, Actor: mustOmnaraActorParams(t, f.UserID),
				})
				require.NoError(t, err)
			} else {
				require.NoError(t, f.Store.Integrations().RevokeIntegrationTargetBinding(ctx, testProjectID, f.binding.ID))
				waitForIntegrationDatabaseTimeAfter(t, ctx, f.Store.pool, request.Deadline)
				_, err := f.Store.Execution().CompleteExternalChannelRequest(ctx, externalRequestCompletion(t, request))
				require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict, "deadline applies before maintenance catches up")
				count, err := f.Store.Execution().ExpireExternalChannelRequests(ctx, 10)
				require.NoError(t, err, "expiration does not require the old grant to remain live")
				require.EqualValues(t, 1, count)
			}
			_, err := newIntegrationStore(f.Store.pool).Execution().CompleteExternalChannelRequest(
				ctx, externalRequestCompletion(t, request))
			require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
			count, err := f.Store.Execution().ExpireExternalChannelRequests(ctx, 10)
			require.NoError(t, err)
			require.Zero(t, count)
			f.requireOneResult(t, ctx)
		})
	}
}

func TestExternalChannelRequestRejectsReplacementBindingBeforeFirstCompletion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newExternalRequestFixture(t, ctx, toolcatalog.ToolNameSendChannelMessage)
	request := f.start(t, ctx, time.Minute)
	require.NoError(t, f.Store.Integrations().RevokeIntegrationTargetBinding(ctx, testProjectID, f.binding.ID))
	_, err := f.Store.Integrations().CreateIntegrationTargetBinding(
		ctx, integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: testProjectID, AgentID: f.AgentID, IntegrationInstallID: request.IntegrationInstallID,
			IntegrationTargetID: f.target.ID, SendAllowed: true, Source: "replacement",
			ReplyChannelGrants: &integrationstore.ChannelGrants{ReceiveAllowed: true, ReadAllowed: true, SendAllowed: true},
		})
	require.NoError(t, err)
	_, err = f.Store.Execution().CompleteExternalChannelRequest(ctx, externalRequestCompletion(t, request))
	require.Error(t, err, "a different eligible binding cannot replace the accepted pin")
	page, err := f.Store.Execution().ListPendingExternalChannelRequests(
		ctx, executionstore.ListExternalChannelRequestsInput{
			ProjectID: testProjectID, IntegrationInstallID: request.IntegrationInstallID, Limit: 10,
		})
	require.NoError(t, err)
	require.Empty(t, page.Requests)
}

func (f externalRequestFixture) presentation(t *testing.T, ctx context.Context, lifetime time.Duration) (
	executionstore.AgentInteractionRecord, executionstore.ExternalChannelRequestRecord,
) {
	t.Helper()
	_, err := executionstore.IntegrationSetAgentIntegrationTarget(ctx, f.Store.q, testProjectID, f.AgentID, f.target.ID)
	require.NoError(t, err)
	interaction := createQuestionInteractionForTest(t, ctx, f.processDaemonFixture, f.call.ID)
	form, err := interaction.Form()
	require.NoError(t, err)
	interactionID, err := publicid.Encode(publicid.KindAgentInteraction, interaction.ID)
	require.NoError(t, err)
	agentID, err := publicid.Encode(publicid.KindAgent, f.AgentID)
	require.NoError(t, err)
	channelID, err := publicid.Encode(publicid.KindIntegrationTarget, f.target.ID)
	require.NoError(t, err)
	payload, err := json.Marshal(channelconnector.InteractionPayload{
		Destination: channelconnector.OperationDestination{
			ImplementationKey: f.definition.ImplementationKey, ProviderRef: f.target.ProviderRef,
			ProviderRefKind: f.target.ProviderRefKind, ProviderMetadata: f.target.ProviderMetadata,
		},
		InteractionID: interactionID, AgentID: agentID, ChannelID: channelID, Kind: "question", Form: form,
	})
	require.NoError(t, err)
	request, err := f.Store.Execution().CreateExternalChannelPresentation(
		ctx, executionstore.CreateExternalChannelPresentationInput{
			ProjectID: testProjectID, AgentID: f.AgentID, InteractionID: interaction.ID,
			Payload: payload, Timeout: lifetime,
		})
	require.NoError(t, err)
	return interaction, request
}

func TestExternalChannelPresentationCompletionAndExpiryLeaveQuestionOpen(t *testing.T) {
	t.Parallel()
	for _, complete := range []bool{true, false} {
		name := "expiry"
		if complete {
			name = "presentation_complete"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			f := newExternalRequestFixture(t, ctx, toolcatalog.ToolNameAskQuestion)
			lifetime := time.Second
			if complete {
				lifetime = time.Minute
			}
			interaction, request := f.presentation(t, ctx, lifetime)
			if complete {
				input := externalRequestCompletion(t, request)
				input.Result.Payload = json.RawMessage(`{"message_id":"prompt-1","metadata":{}}`)
				result, err := f.Store.Execution().CompleteExternalChannelRequest(ctx, input)
				require.NoError(t, err)
				require.Nil(t, result.ToolCall)
			} else {
				waitForIntegrationDatabaseTimeAfter(t, ctx, f.Store.pool, request.Deadline)
				count, err := f.Store.Execution().ExpireExternalChannelRequests(ctx, 10)
				require.NoError(t, err)
				require.EqualValues(t, 1, count)
			}
			var state string
			require.NoError(t, f.Store.pool.QueryRow(ctx,
				`SELECT state FROM agent_interactions WHERE id = $1`, interaction.ID).Scan(&state))
			require.Equal(t, "open", state)
			call, err := f.Store.Execution().GetToolCall(ctx, testProjectID, f.AgentID, f.call.ID)
			require.NoError(t, err)
			require.Equal(t, executionstore.ToolCallStateWaiting, call.State)
		})
	}
}

func TestDashboardQuestionResolutionCancelsPendingExternalCopyAtomically(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newExternalRequestFixture(t, ctx, toolcatalog.ToolNameAskQuestion)
	interaction, request := f.presentation(t, ctx, time.Minute)
	_, err := f.Store.Execution().ResolveAgentInteraction(ctx, executionstore.ResolveAgentInteractionInput{
		ProjectID: testProjectID, AgentID: f.AgentID, ID: interaction.ID,
		Resolution: currentChannelInteractionResolution(), Actor: mustOmnaraActorParams(t, f.UserID),
	})
	require.NoError(t, err)
	stored, err := f.Store.Execution().GetExternalChannelRequest(
		ctx, testProjectID, request.IntegrationInstallID, request.ID)
	require.NoError(t, err)
	require.Equal(t, executionstore.ExternalChannelRequestCanceled, stored.State)
	input := externalRequestCompletion(t, request)
	input.Result.Payload = json.RawMessage(`{"message_id":"late-prompt"}`)
	_, err = f.Store.Execution().CompleteExternalChannelRequest(ctx, input)
	require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
	f.requireOneResult(t, ctx)
}

func TestExternalChannelRequestDeletedConnectionFindsRevokedOwnerAfterCurrentChannelMoves(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newExternalRequestFixture(t, ctx, toolcatalog.ToolNameSendChannelMessage)
	request := f.start(t, ctx, time.Minute)
	_, err := executionstore.IntegrationSetAgentIntegrationTarget(ctx, f.Store.q, testProjectID, f.AgentID, NilID)
	require.NoError(t, err)
	require.NoError(t, f.Store.Integrations().RevokeIntegrationTargetBinding(ctx, testProjectID, f.binding.ID))
	require.NoError(t, f.Store.Integrations().DeleteIntegrationInstall(ctx, testProjectID, request.IntegrationInstallID))
	stored, err := f.Store.Execution().GetExternalChannelRequest(
		ctx, testProjectID, request.IntegrationInstallID, request.ID)
	require.NoError(t, err)
	require.Equal(t, executionstore.ExternalChannelRequestCanceled, stored.State)
	require.Equal(t, "connection_deleted", stored.StateReasonCode)
	call, err := f.Store.Execution().GetToolCall(ctx, testProjectID, f.AgentID, f.call.ID)
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolResultOutcomeCanceled, call.Outcome)
	_, err = f.Store.Execution().CompleteExternalChannelRequest(ctx, externalRequestCompletion(t, request))
	require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
	f.requireOneResult(t, ctx)
}

func TestExternalChannelRequestCompletionRacesCancellationWithoutSecondResult(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newExternalRequestFixture(t, ctx, toolcatalog.ToolNameSendChannelMessage)
	request := f.start(t, ctx, time.Minute)
	input := externalRequestCompletion(t, request)
	start := make(chan struct{})
	var completionErr, cancellationErr error
	var workers sync.WaitGroup
	workers.Go(func() {
		<-start
		_, completionErr = f.Store.Execution().CompleteExternalChannelRequest(ctx, input)
	})
	actor := mustOmnaraActorParams(t, f.UserID)
	workers.Go(func() {
		<-start
		_, cancellationErr = f.Store.Execution().CancelAgent(ctx, executionstore.CancelAgentInput{
			ProjectID: testProjectID, AgentID: f.AgentID, Actor: actor,
		})
	})
	close(start)
	workers.Wait()
	require.NoError(t, cancellationErr)
	if completionErr != nil {
		require.ErrorIs(t, completionErr, storeerr.ErrStateTransitionConflict)
	}
	f.requireOneResult(t, ctx)
}

func TestExternalChannelRequestLaterCapabilityCannotEnableUnacceptedDelegation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newExternalRequestFixture(t, ctx, toolcatalog.ToolNameSendChannelMessage)
	definition := externalDefinitionInput(f.target.IntegrationInstallID)
	definition.SendParamsSchema = json.RawMessage(`{"type":"object"}`)
	definition.Capabilities.CreatesReplyChannel = false
	_, err := f.Store.Integrations().PublishExternalChannelDefinition(ctx, definition)
	require.NoError(t, err)
	f.input.CreatesReplyChannel = false
	request := f.start(t, ctx, time.Minute)
	require.False(t, request.CreatesReplyChannel)
	definition.Capabilities.CreatesReplyChannel = true
	_, err = f.Store.Integrations().PublishExternalChannelDefinition(ctx, definition)
	require.NoError(t, err)
	input := externalRequestCompletion(t, request)
	input.Result.Payload = json.RawMessage(`{"publication":"published","message_channel":"destination",
		"reply_channel":{"implementation_key":"conversation",` +
		`"provider_ref":"unapproved-child","provider_ref_kind":"thread"}}`)
	result, err := f.Store.Execution().CompleteExternalChannelRequest(ctx, input)
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolResultOutcomeFailed, result.ToolCall.Outcome)
	require.Contains(t, string(result.ToolCall.ResultContentParts), "continuation_error")
	var children int
	require.NoError(t, f.Store.pool.QueryRow(ctx,
		`SELECT count(*) FROM integration_targets WHERE parent_channel_id = $1`, f.target.ID).Scan(&children))
	require.Zero(t, children)
}

func TestExternalChannelHistoryResolvesOnlyKnownAuthorizedAddressesWithoutRegistration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newExternalRequestFixture(t, ctx, toolcatalog.ToolNameReadChannel)
	known := channelconnector.ReplyDestination{
		ImplementationKey: f.definition.ImplementationKey, ProviderRef: "known-child", ProviderRefKind: "thread",
	}
	unbound, unknown := known, known
	unbound.ProviderRef, unknown.ProviderRef = "unbound-child", "unknown-child"
	var knownID ID
	for i, ref := range []channelconnector.ReplyDestination{known, unbound} {
		target, err := f.Store.Integrations().CreateIntegrationTarget(ctx, integrationstore.CreateIntegrationTargetInput{
			ProjectID: testProjectID, IntegrationInstallID: f.target.IntegrationInstallID, ChannelDefinitionID: f.definition.ID,
			ParentChannelID: f.target.ID, ProviderRef: ref.ProviderRef, ProviderRefKind: ref.ProviderRefKind,
		})
		require.NoError(t, err)
		if i == 0 {
			knownID = target.ID
			_, err = f.Store.Integrations().CreateIntegrationTargetBinding(ctx,
				integrationstore.CreateIntegrationTargetBindingInput{
					ProjectID: testProjectID, AgentID: f.AgentID, IntegrationInstallID: f.target.IntegrationInstallID,
					IntegrationTargetID: target.ID, ReadAllowed: true, Source: "explicit-history-fixture",
				})
			require.NoError(t, err)
		}
	}
	request := f.start(t, ctx, time.Minute)
	input := externalRequestCompletion(t, request)
	input.Result.Payload = mustExternalHistoryJSON(t, channelconnector.ProviderReadResult{
		Messages: []channelconnector.ProviderMessageObservation{
			{Content: channelconnector.Message{Text: "known"}, Publication: channelconnector.MessagePublished,
				ReplyChannel: &known, ReplyTo: &channelconnector.ProviderMessageReference{MessageID: "previous"}},
			{Content: channelconnector.Message{Text: "unknown reference, retained content"},
				Publication: channelconnector.MessagePublished, ReplyChannel: &unknown,
				ReplyTo: &channelconnector.ProviderMessageReference{MessageID: "unknown", Destination: &unknown}},
			{Content: channelconnector.Message{Text: "unbound reference, retained content"},
				Publication: channelconnector.MessagePublished, ReplyChannel: &unbound},
		},
		NextCursor: "provider cursor", Coverage: channelconnector.HistoryComplete,
	})
	var targetsBefore, bindingsBefore int
	require.NoError(t, f.Store.pool.QueryRow(ctx, `SELECT count(*) FROM integration_targets`).Scan(&targetsBefore))
	require.NoError(t, f.Store.pool.QueryRow(ctx,
		`SELECT count(*) FROM integration_target_bindings`).Scan(&bindingsBefore))
	completed, err := f.Store.Execution().CompleteExternalChannelRequest(ctx, input)
	require.NoError(t, err)
	var blocks []struct {
		Value channelconnector.ReadResult `json:"value"`
	}
	require.NoError(t, json.Unmarshal(completed.ToolCall.ResultContentParts, &blocks))
	require.Len(t, blocks, 1)
	var resultFields []struct {
		Value map[string]json.RawMessage `json:"value"`
	}
	require.NoError(t, json.Unmarshal(completed.ToolCall.ResultContentParts, &resultFields))
	require.NotContains(t, resultFields[0].Value, "status")
	require.NotContains(t, resultFields[0].Value, "request_id")
	history := blocks[0].Value
	require.Len(t, history.Messages, 3)
	channelID, err := publicid.Encode(publicid.KindIntegrationTarget, f.target.ID)
	require.NoError(t, err)
	knownChannelID, err := publicid.Encode(publicid.KindIntegrationTarget, knownID)
	require.NoError(t, err)
	require.Equal(t, knownChannelID, history.Messages[0].ReplyChannelID)
	require.Equal(t, channelID, history.Messages[0].ReplyTo.ChannelID)
	for _, message := range history.Messages {
		require.Equal(t, channelID, message.ChannelID)
		require.NotEmpty(t, message.Content.Text)
	}
	require.Empty(t, history.Messages[1].ReplyChannelID)
	require.Nil(t, history.Messages[1].ReplyTo)
	require.Empty(t, history.Messages[2].ReplyChannelID)
	require.NotEmpty(t, history.NextCursor)
	require.NotEqual(t, "provider cursor", history.NextCursor)
	var targetsAfter, bindingsAfter int
	require.NoError(t, f.Store.pool.QueryRow(ctx, `SELECT count(*) FROM integration_targets`).Scan(&targetsAfter))
	require.NoError(t, f.Store.pool.QueryRow(ctx, `SELECT count(*) FROM integration_target_bindings`).Scan(&bindingsAfter))
	require.Equal(t, targetsBefore, targetsAfter)
	require.Equal(t, bindingsBefore, bindingsAfter)
}

func mustExternalHistoryJSON(t *testing.T, value channelconnector.ProviderReadResult) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return raw
}

func TestExternalChannelHistoryRejectsForeignArtifactsWithoutCompleting(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newExternalRequestFixture(t, ctx, toolcatalog.ToolNameReadChannel)
	foreignAgent := mustCreateAgent(t, ctx, f.Store)
	foreignID, allowedID := uuid.New(), uuid.New()
	for _, artifact := range []struct{ id, agent ID }{{foreignID, foreignAgent}, {allowedID, f.AgentID}} {
		_, err := f.Store.q.InsertArtifact(ctx, dbsqlc.InsertArtifactParams{
			ID: artifact.id, ProjectID: testProjectID, AgentID: artifact.agent, ContentType: "image/png",
		})
		require.NoError(t, err)
	}
	request := f.start(t, ctx, time.Minute)
	input := externalRequestCompletion(t, request)
	for i, artifactID := range []ID{foreignID, uuid.New(), allowedID} {
		publicID, err := publicid.Encode(publicid.KindArtifact, artifactID)
		require.NoError(t, err)
		input.Result.Payload = mustExternalHistoryJSON(t, channelconnector.ProviderReadResult{
			Messages: []channelconnector.ProviderMessageObservation{{
				Content:     channelconnector.Message{ArtifactIDs: []string{publicID}},
				Publication: channelconnector.MessagePublished,
			}}, Coverage: channelconnector.HistoryComplete,
		})
		result, err := f.Store.Execution().CompleteExternalChannelRequest(ctx, input)
		if i < 2 {
			require.ErrorIs(t, err, storeerr.ErrUnauthorized)
			saved, err := f.Store.Execution().GetExternalChannelRequest(
				ctx, testProjectID, request.IntegrationInstallID, request.ID)
			require.NoError(t, err)
			require.Equal(t, executionstore.ExternalChannelRequestPending, saved.State)
			continue
		}
		require.NoError(t, err)
		require.Equal(t, executionstore.ToolResultOutcomeSucceeded, result.ToolCall.Outcome)
		require.Contains(t, string(result.ToolCall.ResultContentParts), publicID)
	}
	f.requireOneResult(t, ctx)
}

func TestExternalChannelRequestRegistrationSQLFailureCommitsPublicationWithoutPartialChild(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var diagnostics bytes.Buffer
	ctx = log.WithLogger(ctx, slog.New(slog.NewJSONHandler(&diagnostics, nil)))
	f := newExternalRequestFixture(t, ctx, toolcatalog.ToolNameSendChannelMessage)
	request := f.start(t, ctx, time.Minute)
	_, err := f.Store.pool.Exec(ctx, `CREATE FUNCTION fail_external_child_binding() RETURNS trigger
		LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected child binding failure'; END $$;
		CREATE TRIGGER fail_external_child_binding BEFORE INSERT ON integration_target_bindings
		FOR EACH ROW EXECUTE FUNCTION fail_external_child_binding()`)
	require.NoError(t, err)
	input := externalRequestCompletion(t, request)
	input.Result.Payload = json.RawMessage(`{"publication":"published","message_channel":"reply_channel",
		"message_id":"published-before-local-failure","reply_channel":{
		"implementation_key":"conversation","provider_ref":"child-sql-failure","provider_ref_kind":"thread"}}`)
	result, err := f.Store.Execution().CompleteExternalChannelRequest(ctx, input)
	require.NoError(t, err, "savepoint rollback must recover from an aborted registration statement")
	require.Equal(t, executionstore.ExternalChannelRequestCompleted, result.Request.State)
	require.Equal(t, executionstore.ToolResultOutcomeFailed, result.ToolCall.Outcome)
	var blocks []struct {
		Value channelconnector.SendMessageResult `json:"value"`
	}
	require.NoError(t, json.Unmarshal(result.ToolCall.ResultContentParts, &blocks))
	require.Len(t, blocks, 1)
	require.Equal(t, channelconnector.MessagePublished, blocks[0].Value.Message.Publication)
	require.Equal(t, "published-before-local-failure", blocks[0].Value.Message.MessageID)
	require.Empty(t, blocks[0].Value.Message.ChannelID)
	require.NotNil(t, blocks[0].Value.ContinuationError)
	require.Contains(t, diagnostics.String(), "injected child binding failure")
	require.NotContains(t, diagnostics.String(), "published-before-local-failure")
	require.NotContains(t, diagnostics.String(), "child-sql-failure")
	var children int
	require.NoError(t, f.Store.pool.QueryRow(ctx,
		`SELECT count(*) FROM integration_targets WHERE parent_channel_id = $1`, f.target.ID).Scan(&children))
	require.Zero(t, children, "a failed binding must roll back its newly inserted target")
	replayed, err := f.Store.Execution().CompleteExternalChannelRequest(ctx, input)
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	f.requireOneResult(t, ctx)
}

func TestExternalChannelNoticeUsesRuntimeOwnerAndNeverCompletesTool(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newExternalRequestFixture(t, ctx, toolcatalog.ToolNameSendChannelMessage)
	input := executionstore.CreateExternalChannelNoticeInput{
		ProjectID: testProjectID, AgentID: f.AgentID, TurnID: f.call.TurnID, RuntimeLockID: uuid.New(),
		ChannelID: f.target.ID, NoticeKey: "runtime-status", Payload: f.input.Payload, Timeout: time.Minute,
	}
	_, err := f.Store.Execution().CreateExternalChannelNotice(ctx, input)
	require.Error(t, err)
	input.RuntimeLockID = f.Lock.ID
	request, err := f.Store.Execution().CreateExternalChannelNotice(ctx, input)
	require.NoError(t, err)
	input.Timeout = 2 * time.Minute
	replayed, err := f.Store.Execution().CreateExternalChannelNotice(ctx, input)
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	require.Equal(t, request.ID, replayed.ID)
	require.True(t, request.Deadline.Equal(replayed.Deadline), "replay cannot extend the deadline")
	completed, err := f.Store.Execution().CompleteExternalChannelRequest(ctx, externalRequestCompletion(t, request))
	require.NoError(t, err)
	require.Nil(t, completed.ToolCall)
	call, err := f.Store.Execution().GetToolCall(ctx, testProjectID, f.AgentID, f.call.ID)
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolCallStateReady, call.State)
	var results int
	require.NoError(t, f.Store.pool.QueryRow(ctx,
		`SELECT count(*) FROM tool_call_results WHERE tool_call_id = $1`, f.call.ID).Scan(&results))
	require.Zero(t, results)
}

func TestExternalChannelConcurrentNoticeAcceptanceKeepsOneRequest(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	f := newExternalRequestFixture(t, ctx, toolcatalog.ToolNameSendChannelMessage)
	input := executionstore.CreateExternalChannelNoticeInput{
		ProjectID: testProjectID, AgentID: f.AgentID, TurnID: f.call.TurnID, RuntimeLockID: f.Lock.ID,
		ChannelID: f.target.ID, NoticeKey: "concurrent-status", Payload: f.input.Payload, Timeout: time.Minute,
	}
	blocker, err := f.Store.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = blocker.Rollback(context.Background()) }()
	_, err = blocker.Exec(ctx, `SELECT id FROM agents WHERE id = $1 FOR UPDATE`, f.AgentID)
	require.NoError(t, err)
	results := make([]executionstore.ExternalChannelRequestRecord, 2)
	errs := make([]error, 2)
	var workers sync.WaitGroup
	for i := range results {
		workers.Go(func() { results[i], errs[i] = f.Store.Execution().CreateExternalChannelNotice(ctx, input) })
	}
	// Both callers have observed no row before waiting for the canonical agent
	// lock. The second must recheck after acquisition, not fail a unique insert.
	integrationdb.WaitForNamedLockWaiters(t, ctx, f.Store.pool, "LockAgentInProject", 2)
	require.NoError(t, blocker.Commit(ctx))
	workers.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, results[0].ID, results[1].ID)
	require.NotEqual(t, results[0].Replayed, results[1].Replayed)
	require.True(t, results[0].Deadline.Equal(results[1].Deadline))
}

func TestExternalChannelPendingListRechecksAcceptedDelegationCapability(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newExternalRequestFixture(t, ctx, toolcatalog.ToolNameSendChannelMessage)
	request := f.start(t, ctx, time.Minute)
	require.True(t, request.CreatesReplyChannel)
	list := executionstore.ListExternalChannelRequestsInput{
		ProjectID: testProjectID, IntegrationInstallID: request.IntegrationInstallID, Limit: 10,
	}
	pending, err := f.Store.Execution().ListPendingExternalChannelRequests(ctx, list)
	require.NoError(t, err)
	require.Len(t, pending.Requests, 1)
	definition := externalDefinitionInput(request.IntegrationInstallID)
	definition.SendParamsSchema = json.RawMessage(`{"type":"object"}`)
	definition.Capabilities.CreatesReplyChannel = false
	_, err = f.Store.Integrations().PublishExternalChannelDefinition(ctx, definition)
	require.NoError(t, err)
	pending, err = f.Store.Execution().ListPendingExternalChannelRequests(ctx, list)
	require.NoError(t, err)
	require.Empty(t, pending.Requests, "poll must honor the accepted payload's delegation requirement")
	_, err = f.Store.Execution().CompleteExternalChannelRequest(ctx, externalRequestCompletion(t, request))
	require.ErrorIs(t, err, storeerr.ErrUnauthorized)
}
