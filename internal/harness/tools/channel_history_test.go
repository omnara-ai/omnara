package tools

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/require"
)

func historyTestScope(t *testing.T) (Turn, integrationstore.ID, string) {
	t.Helper()
	turn := Turn{ProjectID: uuid.New(), AgentID: uuid.New()}
	channelID := uuid.New()
	channel, err := publicid.Encode(publicid.KindIntegrationTarget, channelID)
	require.NoError(t, err)
	return turn, channelID, channel
}

func historyTestRequest(t *testing.T, channel string, limit int, cursor string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(struct {
		ChannelID string `json:"channel_id"`
		Limit     int    `json:"limit,omitempty"`
		Cursor    string `json:"cursor,omitempty"`
	}{channel, limit, cursor})
	require.NoError(t, err)
	return raw
}

func TestChannelHistoryRequestDefaultsBoundsAndOriginalArguments(t *testing.T) {
	t.Parallel()
	turn, channelID, channel := historyTestScope(t)
	raw := json.RawMessage(" \n{\"\\u0063hannel_id\" : \"" + channel + "\"}\t")
	original := bytes.Clone(raw)
	request, err := resolveChannelHistoryRequest(raw, turn)
	require.NoError(t, err)
	require.Equal(t, channelID, request.ChannelID)
	require.Equal(t, 50, request.Limit)
	require.Empty(t, request.ProviderCursor)
	require.Equal(t, original, []byte(raw), "defaults must not rewrite stored tool input")
	for _, limit := range []int{1, 50, 100} {
		request, err := resolveChannelHistoryRequest(historyTestRequest(t, channel, limit, ""), turn)
		require.NoError(t, err)
		require.Equal(t, limit, request.Limit)
	}
}

func TestChannelHistoryRequestRejectsAmbiguousOrInvalidInput(t *testing.T) {
	t.Parallel()
	turn, _, channel := historyTestScope(t)
	prefix := `{"channel_id":"` + channel + `"`
	for _, tt := range []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"null root", "null"},
		{"array root", "[]"},
		{"missing channel", "{}"},
		{"null channel", `{"channel_id":null}`},
		{"empty channel", `{"channel_id":""}`},
		{"wrong channel kind", `{"channel_id":"agt_` + strings.Repeat("a", 26) + `"}`},
		{"channel whitespace", `{"channel_id":" ` + channel + ` "}`},
		{"unknown field", prefix + `,"filter":"all"}`},
		{"case alias", prefix + `,"LIMIT":10}`},
		{"duplicate channel", prefix + `,"channel_id":"` + channel + `"}`},
		{"escaped duplicate", prefix + `,"limit":1,"\u006cimit":2}`},
		{"duplicate cursor", prefix + `,"cursor":"a","cursor":"b"}`},
		{"zero limit", prefix + `,"limit":0}`},
		{"negative limit", prefix + `,"limit":-1}`},
		{"over limit", prefix + `,"limit":101}`},
		{"fractional limit", prefix + `,"limit":1.5}`},
		{"rounded fractional limit", prefix + `,"limit":1.00000000000000000000001}`},
		{"overflow limit", prefix + `,"limit":9223372036854775808}`},
		{"huge exponent", prefix + `,"limit":1e1000000000}`},
		{"null limit", prefix + `,"limit":null}`},
		{"string limit", prefix + `,"limit":"10"}`},
		{"boolean limit", prefix + `,"limit":true}`},
		{"null cursor", prefix + `,"cursor":null}`},
		{"empty cursor", prefix + `,"cursor":""}`},
		{"numeric cursor", prefix + `,"cursor":1}`},
		{"trailing value", prefix + `} {}`},
		{"invalid UTF-8", prefix + ",\"cursor\":\"\xff\"}"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			raw := json.RawMessage(tt.raw)
			original := bytes.Clone(raw)
			request, err := resolveChannelHistoryRequest(raw, turn)
			require.Error(t, err)
			require.Equal(t, channelHistoryRequest{}, request)
			require.Equal(t, original, []byte(raw))
		})
	}
}

func TestChannelHistoryCursorScopesPaginationAndPreservesOpaqueBytes(t *testing.T) {
	t.Parallel()
	turn, channelID, channel := historyTestScope(t)
	provider := "  opaque\x00\n\"<&>\u2028日本語+/=  "
	cursor, err := encodeChannelHistoryCursor(provider, turn, channelID)
	require.NoError(t, err)
	require.NotContains(t, cursor, provider)
	for _, limit := range []int{1, 100} {
		raw := historyTestRequest(t, channel, limit, cursor)
		original := bytes.Clone(raw)
		request, err := resolveChannelHistoryRequest(raw, turn)
		require.NoError(t, err)
		require.Equal(t, provider, request.ProviderCursor)
		require.Equal(t, limit, request.Limit, "page size does not change cursor scope")
		require.Equal(t, original, []byte(raw))
	}
	for _, tt := range []struct {
		name    string
		turn    Turn
		channel integrationstore.ID
	}{
		{"project", Turn{ProjectID: uuid.New(), AgentID: turn.AgentID}, channelID},
		{"agent", Turn{ProjectID: turn.ProjectID, AgentID: uuid.New()}, channelID},
		{"channel", turn, uuid.New()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			otherChannel, err := publicid.Encode(publicid.KindIntegrationTarget, tt.channel)
			require.NoError(t, err)
			request, err := resolveChannelHistoryRequest(historyTestRequest(t, otherChannel, 1, cursor), tt.turn)
			require.EqualError(t, err, "read_channel cursor belongs to a different project, agent, or channel")
			require.Equal(t, channelHistoryRequest{}, request)
			require.NotContains(t, err.Error(), provider)
		})
	}
	empty, err := encodeChannelHistoryCursor("", turn, channelID)
	require.NoError(t, err)
	require.Empty(t, empty, "an exhausted page has no next_cursor")
}

func TestChannelHistoryCursorByteBounds(t *testing.T) {
	t.Parallel()
	turn, channelID, channel := historyTestScope(t)
	for _, provider := range []string{
		strings.Repeat("x", 4096),
		strings.Repeat("\x00", 4096),
		strings.Repeat("界", 1365) + "x",
	} {
		cursor, err := encodeChannelHistoryCursor(provider, turn, channelID)
		require.NoError(t, err)
		require.LessOrEqual(t, len(cursor), 8192)
		request, err := resolveChannelHistoryRequest(historyTestRequest(t, channel, 0, cursor), turn)
		require.NoError(t, err)
		require.Equal(t, provider, request.ProviderCursor)
	}
	for _, provider := range []string{strings.Repeat("x", 4097), strings.Repeat("界", 1366), "\xff"} {
		cursor, err := encodeChannelHistoryCursor(provider, turn, channelID)
		require.Error(t, err)
		require.Empty(t, cursor)
	}
	cursor, err := encodeChannelHistoryCursor("next", turn, channelID)
	require.NoError(t, err)
	payload, err := base64.RawURLEncoding.DecodeString(cursor)
	require.NoError(t, err)
	// A valid wrapper at the exact 8KiB boundary remains accepted. Whitespace is
	// inside the encoded JSON, not in the opaque transport string.
	padded := append(payload, bytes.Repeat([]byte(" "), 6144-len(payload))...)
	atLimit := base64.RawURLEncoding.EncodeToString(padded)
	require.Len(t, atLimit, 8192)
	request, err := resolveChannelHistoryRequest(historyTestRequest(t, channel, 0, atLimit), turn)
	require.NoError(t, err)
	require.Equal(t, "next", request.ProviderCursor)
	overLimit := base64.RawURLEncoding.EncodeToString(append(padded, ' '))
	_, err = resolveChannelHistoryRequest(historyTestRequest(t, channel, 0, overLimit), turn)
	require.EqualError(t, err, "invalid read_channel cursor")
}

func TestChannelHistoryCursorRejectsMalformedScopeAndProviderState(t *testing.T) {
	t.Parallel()
	turn, channelID, channel := historyTestScope(t)
	cursor, err := encodeChannelHistoryCursor("private-provider-marker", turn, channelID)
	require.NoError(t, err)
	payload, err := base64.RawURLEncoding.DecodeString(cursor)
	require.NoError(t, err)
	var valid map[string]any
	require.NoError(t, json.Unmarshal(payload, &valid))
	for _, tt := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"missing scope", func(fields map[string]any) { delete(fields, "project_id") }},
		{"extra field", func(fields map[string]any) { fields["version"] = 1 }},
		{"case alias", func(fields map[string]any) {
			fields["Agent_ID"] = fields["agent_id"]
			delete(fields, "agent_id")
		}},
		{"wrong ID kind", func(fields map[string]any) { fields["project_id"] = channel }},
		{"null ID", func(fields map[string]any) { fields["channel_id"] = nil }},
		{"nil ID", func(fields map[string]any) { fields["channel_id"] = "itgt_" + strings.Repeat("a", 26) }},
		{"empty provider", func(fields map[string]any) { fields["provider_cursor"] = "" }},
		{"null provider", func(fields map[string]any) { fields["provider_cursor"] = nil }},
		{"provider array", func(fields map[string]any) { fields["provider_cursor"] = []int{1} }},
		{"provider base64", func(fields map[string]any) { fields["provider_cursor"] = "!" }},
		{"provider UTF-8", func(fields map[string]any) {
			fields["provider_cursor"] = base64.RawURLEncoding.EncodeToString([]byte{0xff})
		}},
		{"provider over cap", func(fields map[string]any) {
			fields["provider_cursor"] = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte("p"), 4097))
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fields := make(map[string]any, len(valid))
			for key, value := range valid {
				fields[key] = value
			}
			tt.mutate(fields)
			modified, err := json.Marshal(fields)
			require.NoError(t, err)
			_, err = resolveChannelHistoryRequest(
				historyTestRequest(t, channel, 0, base64.RawURLEncoding.EncodeToString(modified)), turn,
			)
			require.EqualError(t, err, "invalid read_channel cursor")
		})
	}
	for _, invalid := range []string{
		"!",
		cursor + "=",
		cursor[:4] + "\n" + cursor[4:],
		base64.RawURLEncoding.EncodeToString([]byte("null")),
		base64.RawURLEncoding.EncodeToString(append(bytes.Clone(payload), []byte(" {}")...)),
		base64.RawURLEncoding.EncodeToString([]byte(strings.Replace(
			string(payload), `"project_id":`, `"project_id":"duplicate","\u0070roject_id":`, 1,
		))),
	} {
		_, err := resolveChannelHistoryRequest(historyTestRequest(t, channel, 0, invalid), turn)
		require.EqualError(t, err, "invalid read_channel cursor")
	}
}

func TestChannelHistoryCursorRequiresRealScopeIDs(t *testing.T) {
	t.Parallel()
	turn, channelID, _ := historyTestScope(t)
	for _, scope := range []struct {
		turn      Turn
		channelID integrationstore.ID
	}{
		{Turn{AgentID: turn.AgentID}, channelID},
		{Turn{ProjectID: turn.ProjectID}, channelID},
		{turn, integrationstore.NilID},
	} {
		cursor, err := encodeChannelHistoryCursor("next", scope.turn, scope.channelID)
		require.EqualError(t, err, "invalid read_channel cursor scope")
		require.Empty(t, cursor)
	}
}

// Fixture encoder for malformed/scope parser tests; production pages are encoded
// by the shared executionstore completion mapper, covered by the executor journey.
// encodeChannelHistoryCursor returns an empty string for an exhausted page.
// This wrapper records pagination scope; it is not an authorization credential.
// The eventual reader must recheck live grants and keep provider pagination tied
// to the authorized destination. Neither layer may use a cursor to select a URL.
func encodeChannelHistoryCursor(providerCursor string, turn Turn, channelID integrationstore.ID) (string, error) {
	if providerCursor == "" {
		return "", nil
	}
	if len(providerCursor) > maxChannelHistoryProviderCursorBytes || !utf8.ValidString(providerCursor) {
		return "", errors.New("invalid read_channel provider cursor")
	}
	project, projectErr := publicid.Encode(publicid.KindProject, turn.ProjectID)
	agent, agentErr := publicid.Encode(publicid.KindAgent, turn.AgentID)
	channel, channelErr := publicid.Encode(publicid.KindIntegrationTarget, channelID)
	if projectErr != nil || agentErr != nil || channelErr != nil {
		return "", errors.New("invalid read_channel cursor scope")
	}
	payload, err := json.Marshal(channelHistoryCursor{
		ProjectID: project, AgentID: agent, ChannelID: channel,
		ProviderCursor: base64.RawURLEncoding.EncodeToString([]byte(providerCursor)),
	})
	if err != nil {
		return "", errors.New("cannot encode read_channel cursor")
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	if len(encoded) > maxChannelHistoryCursorBytes {
		return "", errors.New("read_channel cursor exceeds its size limit")
	}
	return encoded, nil
}
