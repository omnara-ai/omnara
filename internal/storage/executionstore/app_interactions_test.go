package executionstore

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/require"
)

func TestAppInteractionHandlerMatching(t *testing.T) {
	t.Parallel()
	connectionID, targetID := uuid.New(), uuid.New()
	connection, err := publicid.Encode(publicid.KindIntegrationConnection, connectionID)
	require.NoError(t, err)
	base := agentconfig.AppResourceCompiled{
		Definition: appdefinition.Slack, Enabled: true, ConnectionID: connection,
		Scope:              &appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: "C123"}},
		InteractionHandler: &appdefinition.InteractionHandler{Definition: appdefinition.SlackInteractions},
	}
	address := integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:111.222"}
	for _, test := range []struct {
		name   string
		change func(*agentconfig.AppResourceCompiled)
		count  int
	}{
		{"handler only", func(*agentconfig.AppResourceCompiled) {}, 1},
		{"disabled", func(r *agentconfig.AppResourceCompiled) { r.Enabled = false }, 0},
		{"listener only", func(r *agentconfig.AppResourceCompiled) {
			r.InteractionHandler = nil
			r.Listener = &appdefinition.Listener{Events: []string{"message"}}
		}, 0},
		{"other connection", func(r *agentconfig.AppResourceCompiled) {
			r.ConnectionID, _ = publicid.Encode(publicid.KindIntegrationConnection, uuid.New())
		}, 0},
		{"other channel", func(r *agentconfig.AppResourceCompiled) {
			r.Scope = &appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: "C456"}}
		}, 0},
		{"other thread", func(r *agentconfig.AppResourceCompiled) {
			r.Scope = &appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: "C123", ThreadTS: "999.000"}}
		}, 0},
		{"wrong handler", func(r *agentconfig.AppResourceCompiled) {
			r.InteractionHandler = &appdefinition.InteractionHandler{Definition: appdefinition.DiscordInteractions}
		}, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			resource := base
			test.change(&resource)
			matches := matchingInteractionDestinations(
				map[string]agentconfig.AppResourceCompiled{"chat": resource}, targetID, connectionID, "slack", address,
			)
			require.Len(t, matches, test.count)
		})
	}
	resources := map[string]agentconfig.AppResourceCompiled{"z": base, "a": base}
	matches := matchingInteractionDestinations(resources, targetID, connectionID, "slack", address)
	require.Len(t, matches, 2, "overlapping resources remain explicitly selectable")
	require.Equal(t, "a", matches[0].ResourceKey)
	require.Equal(t, "z", matches[1].ResourceKey)
	require.Empty(t, matchingInteractionDestinations(resources, targetID, connectionID, "github", address))
}

func TestAppInteractionScopeUsesCanonicalProviderAddress(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		scope   appdefinition.Scope
		address integrationstore.ConversationAddress
		want    bool
	}{
		{
			"Slack DM", appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: "D123"}},
			integrationstore.ConversationAddress{Kind: "dm", Ref: "D123"}, true,
		},
		{
			"Slack DM kind matters", appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: "D123"}},
			integrationstore.ConversationAddress{Kind: "channel", Ref: "D123"}, false,
		},
		{
			"Slack prefix is not channel identity", appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: "C123"}},
			integrationstore.ConversationAddress{Kind: "thread", Ref: "C1234:111.222"}, false,
		},
		{
			"Slack malformed thread", appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: "C123"}},
			integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:not-a-timestamp"}, false,
		},
		{
			"Discord thread", appdefinition.Scope{Discord: &appdefinition.DiscordScope{GuildID: "10", ChannelID: "20"}},
			integrationstore.ConversationAddress{Kind: "thread", Ref: "20:30"}, true,
		},
		{
			"Discord other thread", appdefinition.Scope{Discord: &appdefinition.DiscordScope{ChannelID: "20", ThreadID: "40"}},
			integrationstore.ConversationAddress{Kind: "thread", Ref: "20:30"}, false,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, test.want, interactionScopeContains(&test.scope, test.address))
		})
	}
}

func TestAppInteractionSnapshotAndReceiptBounds(t *testing.T) {
	t.Parallel()
	destination := InteractionDestination{
		HandlerDefinition: appdefinition.SlackInteractions, ResourceKey: "chat",
		IntegrationTargetID: uuid.New(), ConnectionID: uuid.New(),
		Address: integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:111.222"},
	}
	raw, err := json.Marshal(destination)
	require.NoError(t, err)
	parsed, err := (AgentInteractionRecord{Destination: raw}).CapturedDestination()
	require.NoError(t, err)
	require.Equal(t, destination, *parsed)
	parsed, err = (AgentInteractionRecord{}).CapturedDestination()
	require.NoError(t, err)
	require.Nil(t, parsed)
	for _, raw := range []string{
		`null`, `[]`, `{}`, `{} {}`, `{"unexpected":true}`, strings.Repeat(" ", InteractionDestinationMaxBytes) + `{}`,
	} {
		_, err := (AgentInteractionRecord{Destination: json.RawMessage(raw)}).CapturedDestination()
		require.Error(t, err)
	}
	for _, raw := range []string{
		`null`, `[]`, `{} {}`, `{"id":"bad\u0000id"}`, `{"id":"` + strings.Repeat("x", InteractionReceiptMaxBytes) + `"}`,
	} {
		require.Error(t, validateInteractionObject(json.RawMessage(raw), InteractionReceiptMaxBytes))
	}
	require.NoError(
		t,
		validateInteractionObject(
			json.RawMessage(`{"channel_id":"C123","message_ts":"111.222"}`),
			InteractionReceiptMaxBytes,
		),
	)
}
