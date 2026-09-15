package executionstore

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/require"
)

func TestExternalChannelPayloadPreservesNumbersAndRejectsAmbiguity(t *testing.T) {
	t.Parallel()
	raw, err := normalizeExternalChannelPayload(
		json.RawMessage(`{"large":9007199254740993,"business":{"omnara_channel":"customer"}}`))
	require.NoError(t, err)
	require.Contains(t, string(raw), "9007199254740993")
	require.Contains(t, string(raw), `"omnara_channel":"customer"`)
	for _, value := range []string{`null`, `[]`, `{"x":1,"\u0078":2}`, `{"x":1} {}`} {
		t.Run(value, func(t *testing.T) {
			t.Parallel()
			_, err := normalizeExternalChannelPayload(json.RawMessage(value))
			require.Error(t, err)
		})
	}
}

func TestExternalChannelResultRequiresExactCorrelationAndTerminalOutcome(t *testing.T) {
	t.Parallel()
	id := uuid.New()
	requestID, err := publicid.Encode(publicid.KindExternalChannelRequest, id)
	require.NoError(t, err)
	result := channelconnector.OperationResult{
		RequestID: requestID, Outcome: channelconnector.OperationCompleted,
		Payload: json.RawMessage(`{"counter":9007199254740993}`),
	}
	raw, err := normalizeExternalChannelResult(id, result)
	require.NoError(t, err)
	require.Contains(t, string(raw), "9007199254740993")
	for _, tc := range []struct {
		name   string
		change func(*channelconnector.OperationResult)
	}{
		{"wrong_request", func(r *channelconnector.OperationResult) { r.RequestID = "wrong" }},
		{"receipt", func(r *channelconnector.OperationResult) { r.Outcome = "received" }},
		{"duplicate_key", func(r *channelconnector.OperationResult) { r.Payload = json.RawMessage(`{"x":1,"x":2}`) }},
		{"null_payload", func(r *channelconnector.OperationResult) { r.Payload = json.RawMessage(`null`) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			changed := result
			tc.change(&changed)
			_, err := normalizeExternalChannelResult(id, changed)
			require.Error(t, err)
		})
	}
}

func TestExternalChannelTimeoutMustBePositiveAndBounded(t *testing.T) {
	t.Parallel()
	require.NoError(t, validateExternalChannelTimeout(time.Minute))
	require.Error(t, validateExternalChannelTimeout(0))
	require.Error(t, validateExternalChannelTimeout(-time.Second))
	require.Error(t, validateExternalChannelTimeout(ExternalChannelRequestTimeout+time.Second))
}

func TestExternalChannelAcceptedDelegationRejectsCallerWidening(t *testing.T) {
	t.Parallel()
	grants := &integrationstore.ChannelGrants{ReceiveAllowed: true, SendAllowed: true}
	raw := json.RawMessage(`{"message":{"text":"hello"},"params":{},"reply_channel_grants":` +
		`{"receive":true,"read":true,"send":true}}`)
	_, err := pinExternalReplyGrants(raw, grants)
	require.Error(t, err, "a caller cannot add read access absent from the selected immutable binding")
	pinned, err := pinExternalReplyGrants(json.RawMessage(`{"message":{"text":"hello"},"params":{}}`), grants)
	require.NoError(t, err)
	require.Equal(t, &channelconnector.ChannelGrants{Receive: true, Send: true}, acceptedExternalReplyGrants(pinned))
	_, err = pinExternalReplyGrants(pinned, nil)
	require.Error(t, err, "accepted delegation must not silently lose its pin")
}
