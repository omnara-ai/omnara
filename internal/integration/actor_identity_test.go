package integration

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/require"
)

func TestSlackActorIdentityAcrossBotsAndConversations(t *testing.T) {
	first := integrationstore.IntegrationRecord{
		ID: uuid.New(), IntegrationKind: integrationdefinition.SlackThread, Provider: "slack",
		ProviderTenantID: "T123", ProviderAccountRef: "A123", State: integrationstore.IntegrationStateActive,
		ProviderIdentity: json.RawMessage(`{"bot_user_id":"UBOT"}`),
	}
	var saved executionstore.ActorParams
	for i, workspace := range []string{"T123", "T123", "T_OTHER"} {
		integration := first
		integration.ID = uuid.New()
		integration.ProviderTenantID = workspace
		integration.ProviderAccountRef = "A_DIFFERENT"
		integration.ProviderIdentity = json.RawMessage(`{"bot_user_id":"U_OTHER_BOT"}`)
		envelope := map[string]any{
			"type": "event_callback", "team_id": workspace, "api_app_id": integration.ProviderAccountRef,
			"event_id":       "EvActor",
			"authorizations": []slack.Authorization{{TeamID: workspace, UserID: "U_OTHER_BOT", IsBot: true}},
			"event":          slack.Event{Type: "message", User: "U_PERSON", Channel: "C123", ThreadTS: "1.0", TS: "2.0", Text: "hello"},
		}
		if i == 0 {
			integration = first
			envelope["api_app_id"] = first.ProviderAccountRef
			envelope["authorizations"] = []slack.Authorization{{TeamID: workspace, UserID: "UBOT", IsBot: true}}
			envelope["event"] = slack.Event{Type: "message", User: "U_PERSON", Channel: "D123", TS: "1.0", Text: "hello"}
		}
		raw, err := json.Marshal(envelope)
		require.NoError(t, err)
		event, ok, err := NormalizeSlackIntegrationEvent(integration, raw)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, "Slack", event.Actor.Metadata["source_label"])
		if i == 0 {
			saved = event.Actor
		} else if i == 1 {
			require.Equal(t, saved, event.Actor, "a different bot and conversation share the person")
		} else {
			require.NotEqual(t, saved.ProviderTenantID, event.Actor.ProviderTenantID)
		}
	}
}

func TestDiscordActorIdentityAcrossApplicationsAndGuilds(t *testing.T) {
	var saved executionstore.ActorParams
	for i, application := range []string{"11", "44"} {
		integration := integrationstore.IntegrationRecord{
			ID: uuid.New(), IntegrationKind: integrationdefinition.DiscordThread, Provider: "discord",
			ProviderTenantID: application, ProviderAccountRef: "22", State: integrationstore.IntegrationStateActive,
		}
		message := discord.Message{
			ID: "500", ChannelID: "300", GuildID: "100", Type: 0,
			Author:  discord.User{ID: "33", Username: "alex", GlobalName: "Alex"},
			Content: "hello", Mentions: []discord.User{{ID: "22", Bot: true}},
		}
		if i > 0 {
			integration.ProviderAccountRef = "55"
			message.GuildID, message.ChannelID = "101", "301"
			message.Mentions = []discord.User{{ID: "55", Bot: true}}
		}
		channel := discord.Channel{ID: message.ChannelID, GuildID: message.GuildID, Type: 0}
		data, err := json.Marshal(message)
		require.NoError(t, err)
		raw, err := json.Marshal(discord.Dispatch{Type: "MESSAGE_CREATE", Sequence: 17, Data: data})
		require.NoError(t, err)
		event, ok, err := NormalizeDiscordIntegrationEvent(integration, raw, channel)
		require.NoError(t, err)
		require.True(t, ok)
		if i == 0 {
			saved = event.Actor
		} else {
			require.Equal(t, saved, event.Actor)
		}
	}
}

func TestGitHubActorIdentityAcrossInstallationsAndRenames(t *testing.T) {
	first := integrationstore.IntegrationRecord{
		ID: uuid.New(), IntegrationKind: integrationdefinition.GitHubPR, Provider: "github",
		ProviderTenantID: "123", ProviderAccountRef: "456", State: integrationstore.IntegrationStateActive,
		ProviderIdentity: json.RawMessage(`{"bot_user_id":999,"bot_login":"helper[bot]"}`),
	}
	raw := []byte(`{"action":"created","installation":{"id":456},"repository":{"id":1001,"full_name":"owner/repo"},
		"sender":{"id":71,"login":"person","type":"User"},"issue":{"number":42,"pull_request":{"url":"https://api.github.com/repos/owner/repo/pulls/42"}},
		"comment":{"id":3001,"body":"hello","user":{"id":71,"login":"person","type":"User"}}}`)
	event, ok, err := NormalizeGitHubIntegrationEvent(first, raw)
	require.NoError(t, err)
	require.True(t, ok)
	other := first
	other.ID, other.ProviderAccountRef, other.ProviderTenantID = uuid.New(), "457", "124"
	var payload githubEventPayload
	require.NoError(t, json.Unmarshal(raw, &payload))
	payload.Installation.ID = 457
	payload.Sender.Login, payload.Comment.User.Login = "renamed-person", "renamed-person"
	raw, err = json.Marshal(payload)
	require.NoError(t, err)
	renamed, ok, err := NormalizeGitHubIntegrationEvent(other, raw)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, event.Actor.ProviderTenantID, renamed.Actor.ProviderTenantID)
	require.Equal(t, event.Actor.ProviderUserID, renamed.Actor.ProviderUserID)
	require.Equal(t, "renamed-person", *renamed.Actor.DisplayName)
	require.Equal(t, "GitHub", renamed.Actor.Metadata["source_label"])
}
