//go:build integration

package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/omnara-ai/omnara/internal/apps/slack"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/require"
)

func TestSlackEventsThreadBroadcastContinuesAssignedThreadOnce(t *testing.T) {
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	server := newSlackEventsTestServer(t)
	t.Cleanup(server.Close)
	f := newSlackEventsIntegrationFixture(t, ctx, pool, server, "slack-thread-broadcast")
	send := func(id string, event slack.Event, outcome string) {
		t.Helper()
		raw, err := json.Marshal(map[string]any{
			"type": "event_callback", "team_id": "T123", "api_app_id": "A123", "event_id": id,
			"authorizations": []map[string]any{{"team_id": "T123", "user_id": "U_BOT", "is_bot": true}},
			"event":          event,
		})
		require.NoError(t, err)
		body := string(raw)
		response := requestJSONWithHeaders(t, f.Handler, http.MethodPost, appEventsPath, body, "",
			http.StatusOK, unitSlackSignedHeaders(body, "signing-secret"))
		require.Equal(t, outcome, response["ok"])
		drainSlackJourney(t, ctx, f.Project, f.Slack)
	}
	root := slack.Event{Type: "app_mention", User: "U123", Text: "<@U_BOT> help", Channel: "C123",
		ChannelType: "channel", TS: "111.222", Team: "T123"}
	send("broadcast-root", root, "received")
	target, err := slackJourneyTarget(t, pool, f.Project.Store.Apps(), ctx,
		f.Project.ProjectUUID, f.Install.ID, "C123:111.222")
	require.NoError(t, err)
	reply := slack.Event{Type: "message", Subtype: "thread_broadcast", User: "U123", Text: "follow up without mention",
		Channel: "C123", ChannelType: "channel", TS: "333.444", ThreadTS: root.TS, Team: "T123"}
	send("broadcast-reply", reply, "received")
	_, found, err := f.Project.Store.Execution().GetAppTargetInputByIdempotency(ctx,
		executionstore.GetAppTargetInputByIdempotencyInput{
			AppID: f.Install.ID, AppTargetID: target.ID, IdempotencyKey: "slack:message:T123:C123:333.444",
		})
	require.NoError(t, err)
	require.True(t, found, "an unmentioned broadcast must reach its assigned thread")
	ordinary := reply
	ordinary.Subtype = ""
	send("broadcast-ordinary-delivery", ordinary, "received")
	unassigned := reply
	unassigned.TS, unassigned.ThreadTS = "444.555", "222.333"
	send("broadcast-unassigned-thread", unassigned, "received")
	channelOnly := reply
	channelOnly.TS, channelOnly.ThreadTS, channelOnly.Text = "555.666", "", "<@U_BOT> incomplete broadcast"
	send("broadcast-without-thread", channelOnly, "ignored")
	self := reply
	self.TS, self.User = "666.777", "U_BOT"
	send("broadcast-self", self, "ignored")
	bot := reply
	bot.TS, bot.BotID = "777.888", "B_OTHER"
	send("broadcast-bot", bot, "ignored")
	var agents, inputs, receipts int
	require.NoError(t, pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM agents WHERE project_id=$1),
		(SELECT count(*) FROM agent_inputs WHERE project_id=$1 AND input_kind='content'),
		(SELECT count(*) FROM app_inbox WHERE project_id=$1)`, f.Project.ProjectUUID).
		Scan(&agents, &inputs, &receipts))
	require.Equal(t, 1, agents, "broadcasts must not create another channel-root agent")
	require.Equal(t, 2, inputs, "ordinary and broadcast deliveries of one reply must deduplicate")
	require.Equal(t, 4, receipts, "self, bot and malformed broadcasts must be ignored at HTTP intake")
}
