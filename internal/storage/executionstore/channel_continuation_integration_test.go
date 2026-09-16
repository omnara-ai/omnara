//go:build integration

package executionstore_test

import (
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestManagedSendKeepsPublicationWhenProviderThreadCreationFails(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newManagedOperationFixture(t, ctx, "provider-continuation-failed")
	f.start(t, ctx)
	prepared, err := f.Store.Execution().PrepareChannelOperation(ctx, f.input)
	require.NoError(t, err)
	input := f.completionInput(t, prepared, channelconnector.MessageAtDestination)
	input.Result.Payload, err = json.Marshal(channelconnector.SendResult{
		Publication: channelconnector.MessagePublished, MessageChannel: channelconnector.MessageAtDestination,
		MessageID: "known-discord-message",
		ContinuationError: &channelconnector.ContinuationError{
			Code: "reply_channel_unavailable", Message: "Message exists, but its reply thread is unavailable. Do not resend.",
		},
	})
	require.NoError(t, err)
	first, err := f.Store.Execution().CompleteChannelOperation(ctx, input)
	require.NoError(t, err)
	result := managedSendResult(t, first)
	require.Equal(t, channelconnector.MessagePublished, result.Message.Publication)
	require.Equal(t, "known-discord-message", result.Message.MessageID)
	require.NotEmpty(t, result.Message.ChannelID)
	require.Empty(t, result.Message.ReplyChannelID)
	require.Equal(t, "reply_channel_unavailable", result.ContinuationError.Code)
	require.Equal(t, executionstore.ToolResultOutcomeFailed, first.Outcome)
	var count int
	require.NoError(t, f.Store.pool.QueryRow(ctx,
		`SELECT count(*) FROM integration_targets WHERE project_id=$1 AND integration_install_id=$2`,
		testProjectID, f.binding.IntegrationInstallID).Scan(&count))
	require.Equal(t, 1, count, "an unavailable provider thread creates no child address")
	_, err = f.Store.Execution().CompleteChannelOperation(ctx, input)
	require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
	replayed, err := f.Store.Execution().GetToolCall(ctx, testProjectID, f.AgentID, f.input.ToolCallID)
	require.NoError(t, err)
	require.JSONEq(t, string(first.ResultContentParts), string(replayed.ResultContentParts))
}
