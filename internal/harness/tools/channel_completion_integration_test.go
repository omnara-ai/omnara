//go:build integration

package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

func TestManagedChannelSendPublicationSurvivesCompletionAuthorityFailure(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newIntegrationToolFixture(t, ctx, "send-completion-failed")
	channel := createConnectorToolChannel(t, ctx, f, "send-completion-failed")
	id, err := publicid.Encode(publicid.KindIntegrationTarget, channel.Target.ID)
	require.NoError(t, err)
	call := f.recordToolCall(t, ctx, "send", toolcatalog.ToolNameSendChannelMessage,
		`{"channel_id":"`+id+`","message":{"text":"already published"}}`, f.Now)
	turn := managedToolTurn(f)
	attempts := 0
	executor := Executor{Store: f.Store, ChannelOperations: testChannelOperations(func(
		ioCtx context.Context, request channelconnector.OperationRequest,
	) (channelconnector.OperationResult, error) {
		attempts++
		// The provider accepted the send while its source binding was revoked.
		// Local completion must honor the revocation without forgetting publication.
		if err := f.Store.Integrations().RevokeIntegrationTargetBinding(
			ioCtx, toolsTestProjectID, channel.Binding.ID,
		); err != nil {
			return channelconnector.OperationResult{}, err
		}
		return completedTestChannelSend(request), nil
	})}
	result, err := dispatchAsyncToolToTerminal(t, ctx, executor, turn, call)
	require.NoError(t, err)
	require.Equal(t, DispatchCompleted, result.Disposition)
	var parts []struct {
		Value channelOperationToolResult `json:"value"`
	}
	require.NoError(t, json.Unmarshal(result.ContentParts, &parts))
	require.Len(t, parts, 1)
	observed := parts[0].Value
	require.NotEmpty(t, observed.RequestID)
	require.Equal(t, channelconnector.OperationFailed, observed.Status)
	require.Equal(t, "channel_operation_completion_failed", observed.Code)
	require.Contains(t, observed.Detail, "local completion could not be confirmed")
	require.Contains(t, observed.Detail, "Do not resend")
	require.NotNil(t, observed.Message)
	require.Equal(t, channelconnector.MessagePublished, observed.Message.Publication)
	require.Equal(t, id, observed.Message.ChannelID)
	require.Equal(t, "sent-1", observed.Message.MessageID)
	require.Equal(t, "already published", observed.Message.Content.Text)
	record, err := f.Store.Execution().GetToolCall(ctx, turn.ProjectID, turn.AgentID, f.toolCallID(t, ctx, call.ID))
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolResultOutcomeFailed, record.Outcome)
	require.JSONEq(t, string(call.Input), string(record.Input))
	require.Equal(t, result.ContentParts, record.ResultContentParts)
	replay, err := executor.Dispatch(ctx, turn, call)
	require.NoError(t, err)
	require.Equal(t, record.ResultContentParts, replay.ContentParts)
	require.Equal(t, 1, attempts, "local completion failure must never resend the accepted mutation")
	_, err = f.Store.Integrations().GetActiveSendBindingForTarget(ctx, toolsTestProjectID, f.Agent.ID, channel.Target.ID)
	require.Error(t, err, "recording publication must not restore the revoked grant")
}
