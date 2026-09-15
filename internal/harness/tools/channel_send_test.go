package tools

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/stretchr/testify/require"
)

func TestChannelSendRequestPreservesMessageAndOpaqueParams(t *testing.T) {
	t.Parallel()
	_, _, channel := historyTestScope(t)
	artifact, err := publicid.Encode(publicid.KindArtifact, uuid.New())
	require.NoError(t, err)
	params := `{"issue":9007199254740993,"nested":{"value":"  unchanged  "}}`
	raw := json.RawMessage(" \n" + `{"channel_id":"` + channel + `","message":{"text":"  hello\\nworld  ",` +
		`"artifact_ids":["` + artifact + `"]},"params":` + params + `}`)
	original := bytes.Clone(raw)
	request, err := parseSendChannelMessageRequest(raw)
	require.NoError(t, err)
	require.Equal(t, channel, request.ChannelID)
	require.Equal(t, "  hello\\nworld  ", request.Message.Text)
	require.Equal(t, []string{artifact}, request.Message.ArtifactIDs)
	require.Equal(t, params, string(request.Params))
	require.Equal(t, original, []byte(raw))
	for _, message := range []string{`{"text":"hello"}`, `{"artifact_ids":["` + artifact + `"]}`} {
		request, err := parseSendChannelMessageRequest(json.RawMessage(`{"channel_id":"` + channel + `","message":` + message + `}`))
		require.NoError(t, err)
		require.Nil(t, request.Params, "only omission can select the default params object")
	}
}

func TestChannelSendRequestRejectsAmbiguityNullAndImplicitDestinations(t *testing.T) {
	t.Parallel()
	_, _, channel := historyTestScope(t)
	prefix := `{"channel_id":"` + channel + `"`
	for _, tc := range []struct{ name, raw string }{
		{"root null", `null`},
		{"root array", `[]`},
		{"missing channel", `{"message":{"text":"hello"}}`},
		{"null channel", `{"channel_id":null,"message":{"text":"hello"}}`},
		{"missing message", prefix + `}`},
		{"old text envelope", prefix + `,"text":"hello"}`},
		{"channel alias", `{"Channel_ID":"` + channel + `","message":{"text":"hello"}}`},
		{"duplicate channel", prefix + `,"channel_id":"` + channel + `","message":{"text":"hello"}}`},
		{"null message", prefix + `,"message":null}`},
		{"empty message", prefix + `,"message":{}}`},
		{"null text", prefix + `,"message":{"text":null}}`},
		{"empty text", prefix + `,"message":{"text":""}}`},
		{"text alias", prefix + `,"message":{"Text":"hello"}}`},
		{"duplicate decoded text", prefix + `,"message":{"text":"hello","\u0074ext":"other"}}`},
		{"null artifacts", prefix + `,"message":{"text":"hello","artifact_ids":null}}`},
		{"empty artifacts", prefix + `,"message":{"text":"hello","artifact_ids":[]}}`},
		{"null artifact element", prefix + `,"message":{"artifact_ids":[null]}}`},
		{"remote media", prefix + `,"message":{"url":"https://example.invalid/file"}}`},
		{"null params", prefix + `,"message":{"text":"hello"},"params":null}`},
		{"array params", prefix + `,"message":{"text":"hello"},"params":[]}`},
		{"duplicate params key", prefix + `,"message":{"text":"hello"},"params":{"x":1,"x":2}}`},
		{"trailing JSON", prefix + `,"message":{"text":"hello"}} {}`},
		{"over text byte cap", prefix + `,"message":{"text":"` +
			strings.Repeat("x", channelconnector.MaxMessageTextBytes+1) + `"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw := json.RawMessage(tc.raw)
			original := bytes.Clone(raw)
			request, err := parseSendChannelMessageRequest(raw)
			require.Error(t, err)
			require.Equal(t, channelSendRequest{}, request)
			require.Equal(t, original, []byte(raw))
		})
	}
}

func TestChannelSendRequestBoundsUTF8BytesWithoutTrimming(t *testing.T) {
	t.Parallel()
	_, _, channel := historyTestScope(t)
	text := strings.Repeat("é", channelconnector.MaxMessageTextBytes/2)
	for _, extra := range []string{"", "x"} {
		raw, err := json.Marshal(map[string]any{
			"channel_id": channel, "message": map[string]string{"text": text + extra},
		})
		require.NoError(t, err)
		request, err := parseSendChannelMessageRequest(raw)
		if extra == "" {
			require.NoError(t, err)
			require.Equal(t, text, request.Message.Text)
		} else {
			require.Error(t, err, "schema character limits do not replace the product byte limit")
		}
	}
}
