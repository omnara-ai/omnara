//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

type managedOperationFixture struct {
	processDaemonFixture
	input   executionstore.PrepareChannelOperationInput
	binding integrationstore.CreateIntegrationTargetBindingInput
}

func newManagedOperationFixture(t *testing.T, ctx context.Context, name string) managedOperationFixture {
	t.Helper()
	f, _, claim := newStartedNormalModelCallTestFixture(t, ctx, name)
	_, _, _, install := createChannelLifecycleFixture(t, ctx, f.Store, "managed-op")
	definition, err := f.Store.Integrations().PublishConnectorChannelDefinition(ctx,
		integrationstore.PublishChannelDefinitionInput{
			ProjectID: testProjectID, IntegrationInstallID: install.ID,
			ImplementationKey: "conversation", Kind: integrationstore.ChannelKindExternal,
			SendParamsSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
			Capabilities: integrationstore.ChannelCapabilities{
				Read: true, Send: true, Text: true, CreatesReplyChannel: true,
			},
			ConnectorCapabilities: testChannelCapabilities(testChannelProvider),
		})
	require.NoError(t, err)
	target, err := f.Store.Integrations().CreateIntegrationTarget(ctx, integrationstore.CreateIntegrationTargetInput{
		ProjectID: testProjectID, IntegrationInstallID: install.ID, ChannelDefinitionID: definition.ID,
		ProviderRef: "root", ProviderRefKind: "conversation",
	})
	require.NoError(t, err)
	binding := integrationstore.CreateIntegrationTargetBindingInput{
		ProjectID: testProjectID, AgentID: f.AgentID, IntegrationInstallID: install.ID,
		IntegrationTargetID: target.ID, ReadAllowed: true, SendAllowed: true, Source: "operation-fixture",
		ReplyChannelGrants: &integrationstore.ChannelGrants{ReceiveAllowed: true, SendAllowed: true},
	}
	_, err = f.Store.Integrations().CreateIntegrationTargetBinding(ctx, binding)
	require.NoError(t, err)
	channelID, err := publicid.Encode(publicid.KindIntegrationTarget, target.ID)
	require.NoError(t, err)
	_, calls := recordToolCallBatchForContextTest(t, ctx, f, claim.Context.ID, name,
		[]toolCallForContextTest{{ProviderCallID: "send", Name: toolcatalog.ToolNameSendChannelMessage,
			Input: json.RawMessage(`{"channel_id":"` + channelID + `","message":{"text":"hello"}}`),
			Type:  toolcatalog.ToolTypeBuiltIn}}, f.Now)
	markToolCallReadyForTest(t, ctx, f, calls[0].ID, f.Now)
	return managedOperationFixture{processDaemonFixture: f, binding: binding,
		input: executionstore.PrepareChannelOperationInput{
			ExecuteToolCallInput: executionstore.ExecuteToolCallInput{
				ProjectID: testProjectID, AgentID: f.AgentID, ToolCallID: calls[0].ID, RuntimeLockID: f.Lock.ID,
			},
			TurnID: calls[0].TurnID, ChannelID: target.ID, Operation: integrationstore.ChannelBindingOperationSend,
		}}
}

func (f managedOperationFixture) start(t *testing.T, ctx context.Context) {
	t.Helper()
	result, err := f.Store.Execution().ExecuteToolCall(ctx, f.input.ExecuteToolCallInput,
		func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
			return executionstore.StartToolCallAsync(), nil
		})
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolCallDispositionRunning, result.Disposition)
}

func TestManagedChannelOperationRequiresRunningExactToolOwner(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newManagedOperationFixture(t, ctx, "managed-operation-owner")
	_, err := f.Store.Execution().PrepareChannelOperation(ctx, f.input)
	require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict, "ready is not async admission")
	f.start(t, ctx)
	prepared, err := f.Store.Execution().PrepareChannelOperation(ctx, f.input)
	require.NoError(t, err)
	require.Equal(t, f.input.ChannelID, prepared.Access().ChannelID)
	for _, input := range []executionstore.PrepareChannelOperationInput{
		func() executionstore.PrepareChannelOperationInput {
			p := f.input
			p.Operation = integrationstore.ChannelBindingOperationRead
			return p
		}(),
		func() executionstore.PrepareChannelOperationInput { p := f.input; p.TurnID = uuid.New(); return p }(),
		func() executionstore.PrepareChannelOperationInput {
			p := f.input
			p.RuntimeLockID = uuid.New()
			return p
		}(),
		func() executionstore.PrepareChannelOperationInput { p := f.input; p.AgentID = uuid.New(); return p }(),
		func() executionstore.PrepareChannelOperationInput { p := f.input; p.ProjectID = uuid.New(); return p }(),
		func() executionstore.PrepareChannelOperationInput { p := f.input; p.ToolCallID = uuid.New(); return p }(),
	} {
		_, err := f.Store.Execution().PrepareChannelOperation(ctx, input)
		require.Error(t, err)
	}
	_, err = f.Store.Execution().RecheckChannelOperation(ctx, executionstore.PreparedChannelOperation{})
	require.ErrorIs(t, err, storeerr.ErrUnauthorized)
	otherOwner := newIntegrationStore(f.Store.pool)
	_, err = otherOwner.Execution().RecheckChannelOperation(ctx, prepared)
	require.ErrorIs(t, err, storeerr.ErrUnauthorized, "prepared correlation belongs to its store")
	_, err = f.Store.Execution().CompleteRuntimeToolCall(ctx, executionstore.CompleteRuntimeToolCallInput{
		ProjectID: testProjectID, AgentID: f.AgentID, ID: f.input.ToolCallID, RuntimeLockID: f.Lock.ID,
		Outcome:            executionstore.ToolResultOutcomeSucceeded,
		ResultContentParts: json.RawMessage(`[{"type":"text","text":"already settled"}]`),
	})
	require.NoError(t, err)
	_, err = f.Store.Execution().RecheckChannelOperation(ctx, prepared)
	require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict, "a completed tool cannot dispatch again")
}

func TestManagedChannelOperationPinsBindingAndDoesNotUnionOrReviveGrants(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newManagedOperationFixture(t, ctx, "managed-operation-binding")
	f.start(t, ctx)
	prepared, err := f.Store.Execution().PrepareChannelOperation(ctx, f.input)
	require.NoError(t, err)
	pin := prepared.Binding()
	require.Equal(t, f.binding.ReplyChannelGrants, pin.ReplyChannelGrants)
	pin.ReplyChannelGrants.ReadAllowed = true
	access := prepared.Access()
	access.SendParamsSchema[0] = '['
	require.False(t, prepared.Binding().ReplyChannelGrants.ReadAllowed, "returned values cannot mutate the retained pin")
	require.JSONEq(t, `{"type":"object","additionalProperties":false}`, string(prepared.Access().SendParamsSchema))
	alternate := f.binding
	alternate.Source = "another-live-sender"
	alternate.ReplyChannelGrants = &integrationstore.ChannelGrants{ReadAllowed: true}
	_, err = f.Store.Integrations().CreateIntegrationTargetBinding(ctx, alternate)
	require.NoError(t, err, "preparation committed and holds no lock across provider I/O")
	live, err := f.Store.Execution().RecheckChannelOperation(ctx, prepared)
	require.NoError(t, err)
	require.True(t, live.Capabilities.Send)
	require.NoError(t, f.Store.Integrations().RevokeIntegrationTargetBinding(ctx, testProjectID, prepared.Binding().ID))
	_, err = f.Store.Execution().RecheckChannelOperation(ctx, prepared)
	require.ErrorIs(t, err, storeerr.ErrNotFound, "a still-live alternate cannot replace the revoked source")
}

func TestManagedChannelOperationRejectsCanceledRuntime(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newManagedOperationFixture(t, ctx, "managed-operation-cancel")
	f.start(t, ctx)
	prepared, err := f.Store.Execution().PrepareChannelOperation(ctx, f.input)
	require.NoError(t, err)
	_, err = f.Store.pool.Exec(ctx,
		`UPDATE agent_runtime_locks SET cancel_requested_at = statement_timestamp() WHERE id = $1`, f.Lock.ID)
	require.NoError(t, err)
	_, err = f.Store.Execution().RecheckChannelOperation(ctx, prepared)
	require.ErrorIs(t, err, storeerr.ErrRuntimeLockInactive)
}

func (f managedOperationFixture) completionInput(
	t *testing.T,
	prepared executionstore.PreparedChannelOperation,
	location channelconnector.MessageLocation,
) executionstore.CompleteChannelOperationInput {
	t.Helper()
	access := prepared.Access()
	payload, err := json.Marshal(channelconnector.SendPayload{
		Destination: channelconnector.OperationDestination{
			ImplementationKey: access.ImplementationKey, ProviderRef: access.ProviderRef,
			ProviderRefKind: access.ProviderRefKind, ProviderMetadata: access.ProviderMetadata,
		},
		Message: channelconnector.Message{Text: "hello"}, Params: json.RawMessage(`{}`),
	})
	require.NoError(t, err)
	result, err := json.Marshal(channelconnector.SendResult{
		Publication: channelconnector.MessagePublished, MessageChannel: location, MessageID: "published-123",
		ReplyChannel: &channelconnector.ReplyDestination{
			ImplementationKey: access.ImplementationKey, ProviderRef: "reply-thread", ProviderRefKind: "thread",
		},
	})
	require.NoError(t, err)
	requestID, err := publicid.Encode(publicid.KindToolCall, f.input.ToolCallID)
	require.NoError(t, err)
	return executionstore.CompleteChannelOperationInput{
		Prepared: prepared, Payload: payload,
		Result: channelconnector.OperationResult{
			RequestID: requestID, Outcome: channelconnector.OperationCompleted, Payload: result,
		},
	}
}

func managedSendResult(t *testing.T, record executionstore.ToolCallRecord) channelconnector.SendMessageResult {
	t.Helper()
	var parts []struct {
		Type  string          `json:"type"`
		Value json.RawMessage `json:"value"`
	}
	require.NoError(t, json.Unmarshal(record.ResultContentParts, &parts))
	require.Len(t, parts, 1)
	require.Equal(t, "structured_data", parts[0].Type)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(parts[0].Value, &fields))
	require.NotContains(t, fields, "status", "known send results use the canonical result schema")
	var result channelconnector.SendMessageResult
	require.NoError(t, json.Unmarshal(parts[0].Value, &result))
	return result
}

func TestManagedChannelCompletionAtomicallyRegistersAndSettlesTool(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newManagedOperationFixture(t, ctx, "managed-complete")
	f.start(t, ctx)
	prepared, err := f.Store.Execution().PrepareChannelOperation(ctx, f.input)
	require.NoError(t, err)
	input := f.completionInput(t, prepared, channelconnector.MessageAtDestination)
	record, err := f.Store.Execution().CompleteChannelOperation(ctx, input)
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolCallStateCompleted, record.State)
	require.Equal(t, executionstore.ToolResultOutcomeSucceeded, record.Outcome)
	result := managedSendResult(t, record)
	require.Equal(t, input.Result.RequestID, result.RequestID)
	require.Equal(t, "hello", result.Message.Content.Text)
	require.Equal(t, channelconnector.MessagePublished, result.Message.Publication)
	parentID, err := publicid.Encode(publicid.KindIntegrationTarget, f.input.ChannelID)
	require.NoError(t, err)
	require.Equal(t, parentID, result.Message.ChannelID, "opening a reply thread does not move the root publication")
	childID, err := publicid.Decode(publicid.KindIntegrationTarget, result.Message.ReplyChannelID)
	require.NoError(t, err)
	child, err := f.Store.Integrations().GetAgentChannelAccess(ctx, testProjectID, f.AgentID, childID)
	require.NoError(t, err)
	require.Equal(t, f.input.ChannelID, child.ParentChannelID)
	require.True(t, child.ReceiveAllowed)
	require.True(t, child.Capabilities.Send)
	require.False(t, child.Capabilities.Read)
	require.False(t, child.Capabilities.CreatesReplyChannel, "child ordinary grants do not delegate onward")
	require.Nil(t, result.ContinuationError)
	// The generic async release path acknowledges a tool already settled by its
	// transactional operation owner, without creating another result or wait.
	require.NoError(t, f.Store.Execution().ReleaseToolCallRuntimeOwnership(ctx,
		executionstore.ReleaseToolCallRuntimeOwnershipInput{
			ProjectID: testProjectID, AgentID: f.AgentID, ToolCallID: f.input.ToolCallID, RuntimeLockID: f.Lock.ID,
		}))
	loaded, err := f.Store.Execution().GetToolCall(ctx, testProjectID, f.AgentID, f.input.ToolCallID)
	require.NoError(t, err)
	require.Equal(t, record.ToolCallResultID, loaded.ToolCallResultID)
	require.Equal(t, executionstore.ToolCallStateCompleted, loaded.State)
}

func TestManagedChannelCompletionDoesNotReviveRevokedChild(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newManagedOperationFixture(t, ctx, "managed-child-revoked")
	f.start(t, ctx)
	prepared, err := f.Store.Execution().PrepareChannelOperation(ctx, f.input)
	require.NoError(t, err)
	access := prepared.Access()
	child, err := f.Store.Integrations().CreateIntegrationTarget(ctx, integrationstore.CreateIntegrationTargetInput{
		ProjectID: testProjectID, IntegrationInstallID: access.IntegrationInstallID,
		ChannelDefinitionID: access.DefinitionID, ParentChannelID: access.ChannelID,
		ProviderRef: "reply-thread", ProviderRefKind: "thread",
	})
	require.NoError(t, err)
	grant := f.binding
	grant.IntegrationTargetID, grant.Source, grant.ReplyChannelGrants = child.ID, "explicit-child-setup", nil
	binding, err := f.Store.Integrations().CreateIntegrationTargetBinding(ctx, grant)
	require.NoError(t, err)
	require.NoError(t, f.Store.Integrations().RevokeIntegrationTargetBinding(ctx, testProjectID, binding.ID))
	record, err := f.Store.Execution().CompleteChannelOperation(ctx,
		f.completionInput(t, prepared, channelconnector.MessageAtReplyChannel))
	require.NoError(t, err, "registration failure must preserve the known publication")
	require.Equal(t, executionstore.ToolResultOutcomeFailed, record.Outcome)
	result := managedSendResult(t, record)
	require.Equal(t, channelconnector.MessagePublished, result.Message.Publication)
	require.Equal(t, "published-123", result.Message.MessageID)
	require.Empty(t, result.Message.ChannelID, "do not substitute the parent for an unavailable containing child")
	require.Empty(t, result.Message.ReplyChannelID)
	require.NotNil(t, result.ContinuationError)
	var live int
	require.NoError(t, f.Store.pool.QueryRow(ctx, `SELECT count(*) FROM integration_target_bindings
		WHERE project_id = $1 AND agent_id = $2 AND integration_target_id = $3 AND revoked_at IS NULL`,
		testProjectID, f.AgentID, child.ID).Scan(&live))
	require.Zero(t, live)
}

func TestManagedChannelOperationLaterCapabilityCannotEnableUnacceptedDelegation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newManagedOperationFixture(t, ctx, "managed-no-later-delegation")
	definition := integrationstore.PublishChannelDefinitionInput{
		ProjectID: testProjectID, IntegrationInstallID: f.binding.IntegrationInstallID,
		ImplementationKey: "conversation", Kind: integrationstore.ChannelKindExternal,
		SendParamsSchema:      json.RawMessage(`{"type":"object","additionalProperties":false}`),
		Capabilities:          integrationstore.ChannelCapabilities{Read: true, Send: true, Text: true},
		ConnectorCapabilities: testChannelCapabilities(testChannelProvider),
	}
	_, err := f.Store.Integrations().PublishConnectorChannelDefinition(ctx, definition)
	require.NoError(t, err)
	f.start(t, ctx)
	prepared, err := f.Store.Execution().PrepareChannelOperation(ctx, f.input)
	require.NoError(t, err)
	require.False(t, prepared.Access().Capabilities.CreatesReplyChannel)
	require.NotNil(t, prepared.Binding().ReplyChannelGrants, "isolate capability widening from a grant change")

	definition.Capabilities.CreatesReplyChannel = true
	_, err = f.Store.Integrations().PublishConnectorChannelDefinition(ctx, definition)
	require.NoError(t, err)
	live, err := f.Store.Integrations().GetAgentChannelAccess(ctx, testProjectID, f.AgentID, f.input.ChannelID)
	require.NoError(t, err)
	require.True(t, live.Capabilities.CreatesReplyChannel)
	rechecked, err := f.Store.Execution().RecheckChannelOperation(ctx, prepared)
	require.NoError(t, err)
	require.False(t, rechecked.Capabilities.CreatesReplyChannel, "dispatch cannot acquire later delegation")

	record, err := f.Store.Execution().CompleteChannelOperation(ctx,
		f.completionInput(t, prepared, channelconnector.MessageAtDestination))
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolResultOutcomeFailed, record.Outcome)
	result := managedSendResult(t, record)
	require.Equal(t, channelconnector.MessagePublished, result.Message.Publication)
	require.Equal(t, "published-123", result.Message.MessageID)
	require.Empty(t, result.Message.ReplyChannelID)
	require.NotNil(t, result.ContinuationError)
	var children int
	require.NoError(t, f.Store.pool.QueryRow(ctx,
		`SELECT count(*) FROM integration_targets WHERE parent_channel_id = $1`, f.input.ChannelID).Scan(&children))
	require.Zero(t, children, "completion cannot acquire later delegation either")
}

func TestManagedChannelCompletionRollsBackChildWhenToolSettlementFails(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newManagedOperationFixture(t, ctx, "managed-complete-rollback")
	f.start(t, ctx)
	prepared, err := f.Store.Execution().PrepareChannelOperation(ctx, f.input)
	require.NoError(t, err)
	_, err = f.Store.pool.Exec(ctx, `CREATE FUNCTION fail_managed_operation_completion() RETURNS trigger
		LANGUAGE plpgsql AS $$ BEGIN
		IF NEW.state = 'completed' THEN RAISE EXCEPTION 'injected tool settlement failure'; END IF;
		RETURN NEW; END $$;
		CREATE TRIGGER fail_managed_operation_completion BEFORE UPDATE ON tool_calls
		FOR EACH ROW EXECUTE FUNCTION fail_managed_operation_completion()`)
	require.NoError(t, err)
	_, err = f.Store.Execution().CompleteChannelOperation(ctx,
		f.completionInput(t, prepared, channelconnector.MessageAtDestination))
	require.ErrorContains(t, err, "injected tool settlement failure")
	var children int
	require.NoError(t, f.Store.pool.QueryRow(ctx, `SELECT count(*) FROM integration_targets
		WHERE project_id = $1 AND integration_install_id = $2 AND provider_ref = 'reply-thread'`,
		testProjectID, prepared.Access().IntegrationInstallID).Scan(&children))
	require.Zero(t, children, "child registration and tool settlement must roll back together")
	call, err := f.Store.Execution().GetToolCall(ctx, testProjectID, f.AgentID, f.input.ToolCallID)
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolCallStateRunning, call.State)
	require.Equal(t, NilID, call.ToolCallResultID)
}
