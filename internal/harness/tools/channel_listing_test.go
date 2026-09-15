package tools

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/require"
)

func TestChannelListCursorBindsAgentAndParentFilter(t *testing.T) {
	t.Parallel()
	turn := Turn{ProjectID: uuid.New(), AgentID: uuid.New()}
	parent, err := publicid.Encode(publicid.KindIntegrationTarget, uuid.New())
	require.NoError(t, err)
	last := integrationstore.AgentChannelTargetCursor{ID: uuid.New(), CreatedAt: time.Now().UTC()}
	raw, err := encodeChannelListCursor(last, turn, parent)
	require.NoError(t, err)
	cursor, err := decodeChannelListCursor(raw)
	require.NoError(t, err)
	require.True(t, cursor.matches(turn, parent))
	require.False(t, cursor.matches(turn, ""), "removing a parent filter changes the page scope")
	require.False(t, cursor.matches(Turn{ProjectID: uuid.New(), AgentID: turn.AgentID}, parent))
	require.False(t, cursor.matches(Turn{ProjectID: turn.ProjectID, AgentID: uuid.New()}, parent))
	input, err := json.Marshal(channelListRequest{Cursor: raw, ParentChannelID: parent, Limit: 1})
	require.NoError(t, err)
	request, page, err := resolveChannelListRequest(input)
	require.NoError(t, err)
	require.Equal(t, parent, request.ParentChannelID)
	require.Equal(t, last.ID, page.After.ID)
	require.True(t, last.CreatedAt.Equal(page.After.CreatedAt))
	require.NotEqual(t, uuid.Nil, page.ParentChannelID)
	authorization, err := marshalJSON(request)
	require.NoError(t, err)
	require.JSONEq(t, string(input), string(authorization), "parsed state must not change the permission request")

	for _, tt := range []struct {
		name   string
		turn   Turn
		parent string
	}{
		{name: "agent", turn: Turn{ProjectID: turn.ProjectID, AgentID: uuid.New()}, parent: parent},
		{name: "project", turn: Turn{ProjectID: uuid.New(), AgentID: turn.AgentID}, parent: parent},
		{name: "parent filter", turn: turn},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input, err := json.Marshal(channelListRequest{Cursor: raw, ParentChannelID: tt.parent})
			require.NoError(t, err)
			// No reader: scope mismatches must fail before authorization or a DB query.
			result, err := listChannels(t.Context(), transactionalToolContext{
				Turn: tt.turn, Call: model.ToolCall{Input: input},
			})
			require.EqualError(t, err, "list_channels cursor belongs to a different agent or parent filter")
			require.Nil(t, result)
		})
	}
}

func TestListChannelsRejectsInvalidCursorBeforeQuery(t *testing.T) {
	t.Parallel()
	channelID, err := publicid.Encode(publicid.KindIntegrationTarget, uuid.New())
	require.NoError(t, err)
	agentID, err := publicid.Encode(publicid.KindAgent, uuid.New())
	require.NoError(t, err)
	for _, tt := range []struct {
		name   string
		cursor string
	}{
		{name: "base64", cursor: "!"},
		{name: "json", cursor: base64.RawURLEncoding.EncodeToString([]byte(`{`))},
		{name: "wrong ID kind", cursor: base64.RawURLEncoding.EncodeToString([]byte(
			`{"created_at":"2026-01-01T00:00:00Z","channel_id":"` + agentID + `"}`,
		))},
		{name: "missing timestamp", cursor: base64.RawURLEncoding.EncodeToString([]byte(
			`{"channel_id":"` + channelID + `"}`,
		))},
		{name: "decoded size", cursor: base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat(" ", 513)))},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input, err := json.Marshal(channelListRequest{Cursor: tt.cursor})
			require.NoError(t, err)
			result, err := listChannels(t.Context(), transactionalToolContext{Call: model.ToolCall{Input: input}})
			require.EqualError(t, err, "invalid list_channels cursor")
			require.Nil(t, result)
		})
	}
}
