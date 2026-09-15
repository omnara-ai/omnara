//go:build integration

package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

func TestExternalChannelToolsWaitForCustomerCompletionWithoutManagedIO(t *testing.T) {
	ctx := context.Background()
	f := newIntegrationToolFixture(t, ctx, "external-tools")
	install, err := f.Store.Integrations().CreateExternalIntegrationInstall(ctx,
		integrationstore.CreateExternalIntegrationInstallInput{
			OrgID: toolsTestOrgID, ProjectID: toolsTestProjectID,
			InstalledBy: toolsTestUserPrincipal(f.User.ID), DisplayName: "Customer connector", Metadata: json.RawMessage(`{}`),
		})
	require.NoError(t, err)
	definition, err := f.Store.Integrations().PublishExternalChannelDefinition(ctx,
		integrationstore.PublishChannelDefinitionInput{
			ProjectID: toolsTestProjectID, IntegrationInstallID: install.ID,
			ImplementationKey: "customer-room", Kind: integrationstore.ChannelKindExternal,
			SendParamsSchema: json.RawMessage(`{"type":"object"}`),
			Capabilities:     integrationstore.ChannelCapabilities{Read: true, Send: true, Text: true},
		})
	require.NoError(t, err)
	channel, err := f.Store.Integrations().CreateIntegrationTarget(ctx, integrationstore.CreateIntegrationTargetInput{
		ProjectID: toolsTestProjectID, IntegrationInstallID: install.ID, ChannelDefinitionID: definition.ID,
		ProviderRef: "customer-room-1", ProviderRefKind: "room",
	})
	require.NoError(t, err)
	binding, err := f.Store.Integrations().CreateIntegrationTargetBinding(ctx,
		integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: toolsTestProjectID, AgentID: f.Agent.ID, IntegrationInstallID: install.ID,
			IntegrationTargetID: channel.ID, ReadAllowed: true, SendAllowed: true, Source: "customer-setup",
		})
	require.NoError(t, err)
	id, err := publicid.Encode(publicid.KindIntegrationTarget, channel.ID)
	require.NoError(t, err)
	calls := []model.ToolCall{
		{ID: "send", Name: toolcatalog.ToolNameSendChannelMessage,
			Input: json.RawMessage(`{"channel_id":"` + id + `","message":{"text":"hello"}}`)},
		{ID: "read", Name: toolcatalog.ToolNameReadChannel, Input: json.RawMessage(`{"channel_id":"` + id + `"}`)},
	}
	f.recordToolCalls(t, ctx, calls, f.Now)
	turn := managedToolTurn(f)
	// No managed client is configured. Both real builtin commands must enter the
	// customer's durable wait, with neither background provider execution nor MCP.
	executor := Executor{Store: f.Store}
	for _, call := range calls {
		_, err := executor.Dispatch(ctx, turn, call)
		require.NoError(t, err)
		record, err := f.Store.Execution().GetToolCall(ctx, turn.ProjectID, turn.AgentID, f.toolCallID(t, ctx, call.ID))
		require.NoError(t, err)
		require.Equal(t, executionstore.ToolCallStateWaiting, record.State)
		require.Equal(t, toolcatalog.ToolTypeBuiltIn, record.Type)
		require.JSONEq(t, string(call.Input), string(record.Input))
	}
	page, err := f.Store.Execution().ListPendingExternalChannelRequests(ctx,
		executionstore.ListExternalChannelRequestsInput{
			ProjectID: toolsTestProjectID, IntegrationInstallID: install.ID, Limit: 10,
		})
	require.NoError(t, err)
	require.Len(t, page.Requests, 2)
	for _, request := range page.Requests {
		require.Equal(t, binding.ID, request.IntegrationTargetBindingID)
		require.WithinDuration(t, request.CreatedAt.Add(executionstore.ExternalChannelRequestTimeout),
			request.Deadline, time.Second)
		requestID, err := publicid.Encode(publicid.KindExternalChannelRequest, request.ID)
		require.NoError(t, err)
		result := completedTestChannelSend(channelconnector.OperationRequest{RequestID: requestID})
		if request.Operation == channelconnector.OperationRead {
			var payload channelconnector.ReadPayload
			require.NoError(t, json.Unmarshal(request.Payload, &payload))
			require.Equal(t, 50, payload.Limit)
			result.Payload = json.RawMessage(`{"messages":[],"coverage":"complete"}`)
		}
		_, err = f.Store.Execution().CompleteExternalChannelRequest(ctx, executionstore.CompleteExternalChannelRequestInput{
			ProjectID: toolsTestProjectID, IntegrationInstallID: install.ID, ID: request.ID, Result: result,
		})
		require.NoError(t, err)
	}
	for _, call := range calls {
		result, err := executor.Dispatch(ctx, turn, call)
		require.NoError(t, err)
		require.Equal(t, DispatchCompleted, result.Disposition)
		body := toolResultMapFromTestParts(t, result.ContentParts)
		require.NotContains(t, body, "status")
		if call.Name == toolcatalog.ToolNameReadChannel {
			require.Equal(t, "complete", body["coverage"])
		} else {
			require.Contains(t, body, "message")
		}
	}
}
