package tools

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/stretchr/testify/require"
)

func TestChannelTransportFailuresPreserveUnknownOutcomeAndHideDiagnostics(t *testing.T) {
	t.Parallel()
	const requestID = "operation-id"
	const secret = "credential-marker-must-never-appear"
	for _, tc := range []struct {
		name   string
		result channelconnector.OperationResult
		err    error
		want   channelconnector.OperationOutcome
	}{
		{
			name: "correlated definite failure",
			result: channelconnector.OperationResult{
				RequestID: requestID, Outcome: channelconnector.OperationFailed,
				Payload: json.RawMessage(`{"provider_error":"` + secret + `"}`),
			},
			want: channelconnector.OperationFailed,
		},
		{
			name: "typed permanent rejection",
			err:  &channelconnector.OperationError{Outcome: channelconnector.OperationFailed, Code: secret},
			want: channelconnector.OperationFailed,
		},
		{
			name: "typed unknown overrides result",
			result: channelconnector.OperationResult{
				RequestID: requestID, Outcome: channelconnector.OperationFailed,
			},
			err:  &channelconnector.OperationError{Outcome: channelconnector.OperationUnknown, Code: secret},
			want: channelconnector.OperationUnknown,
		},
		{
			name: "unclassified error cannot prove no mutation",
			result: channelconnector.OperationResult{
				RequestID: requestID, Outcome: channelconnector.OperationFailed,
			},
			err: errors.New(secret), want: channelconnector.OperationUnknown,
		},
		{
			name: "unrelated failure cannot prove no mutation",
			result: channelconnector.OperationResult{
				RequestID: "other-operation", Outcome: channelconnector.OperationFailed,
			},
			want: channelconnector.OperationUnknown,
		},
		{name: "missing response", want: channelconnector.OperationUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			phase, err := channelTransportFailure(requestID, tc.result, tc.err)
			require.NoError(t, err)
			failed, ok := phase.(failAsync)
			require.True(t, ok, "failed and unknown operations both finish the tool; neither waits for background delivery")
			parts, err := failed.content.contentParts()
			require.NoError(t, err)
			var content []struct {
				Value channelOperationToolResult `json:"value"`
			}
			require.NoError(t, json.Unmarshal(parts, &content))
			require.Len(t, content, 1)
			require.Equal(t, requestID, content[0].Value.RequestID)
			require.Equal(t, tc.want, content[0].Value.Status)
			require.NotContains(t, string(parts), secret)
			require.NotContains(t, failed.cause.Error(), secret)
			if tc.want == channelconnector.OperationUnknown {
				require.Contains(t, content[0].Value.Detail, "do not assume it is safe to resend")
			}
		})
	}
}

func TestChannelSendCompletionFailurePreservesActualPublicationLocation(t *testing.T) {
	t.Parallel()
	_, _, channel := historyTestScope(t)
	for _, test := range []struct {
		name        string
		publication channelconnector.MessagePublication
		location    channelconnector.MessageLocation
		reply       *channelconnector.ReplyDestination
	}{
		{"published without reply", channelconnector.MessagePublished, channelconnector.MessageAtDestination, nil},
		{"draft without reply", channelconnector.MessageDraft, channelconnector.MessageAtDestination, nil},
		{"published with reply", channelconnector.MessagePublished, channelconnector.MessageAtDestination,
			&channelconnector.ReplyDestination{ProviderRef: "private-child-address"}},
		{"published in child", channelconnector.MessagePublished, channelconnector.MessageAtReplyChannel,
			&channelconnector.ReplyDestination{ProviderRef: "private-child-address"}},
		{"draft in child", channelconnector.MessageDraft, channelconnector.MessageAtReplyChannel,
			&channelconnector.ReplyDestination{ProviderRef: "private-child-address"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			prepared := preparedChannelOperation{request: channelconnector.OperationRequest{
				RequestID: "known-send", Scope: channelconnector.OperationScope{ChannelID: channel},
			}}
			phase, err := channelSendCompletionFailure(prepared, channelconnector.Message{Text: "published content"},
				channelconnector.SendResult{
					Publication: test.publication, MessageChannel: test.location, MessageID: "provider-message",
					ReplyChannel: test.reply,
				})
			require.NoError(t, err)
			failed, ok := phase.(failAsync)
			require.True(t, ok)
			parts, err := failed.content.contentParts()
			require.NoError(t, err)
			var content []struct {
				Value channelOperationToolResult `json:"value"`
			}
			require.NoError(t, json.Unmarshal(parts, &content))
			require.Len(t, content, 1)
			result := content[0].Value
			require.Equal(t, "known-send", result.RequestID)
			require.Equal(t, channelconnector.OperationFailed, result.Status)
			require.Equal(t, "channel_operation_completion_failed", result.Code)
			require.Contains(t, result.Detail, "local completion could not be confirmed")
			require.Contains(t, result.Detail, "Do not resend")
			require.NotNil(t, result.Message)
			require.Equal(t, test.publication, result.Message.Publication)
			require.Equal(t, "published content", result.Message.Content.Text)
			require.Equal(t, "provider-message", result.Message.MessageID)
			if test.reply != nil {
				require.Contains(t, result.Detail, "Reply channel registration could not be confirmed")
			}
			require.Empty(t, result.Message.ReplyChannelID)
			require.NotContains(t, string(parts), "private-child-address")
			if test.location == channelconnector.MessageAtDestination {
				require.Equal(t, channel, result.Message.ChannelID)
			} else {
				require.Empty(t, result.Message.ChannelID, "a child publication must never claim its parent contains the message")
			}
		})
	}
}
