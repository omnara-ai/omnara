package appdefinition

import (
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/jsonschema"
	"github.com/stretchr/testify/require"
)

func TestRegistryAndTypedDestinations(t *testing.T) {
	for _, test := range []struct{ id, provider, config, args, kind, key string }{
		{Slack, ProviderSlack, `{"channel_id":"C123"}`, `{"thread_ts":"111.222"}`, "thread", "C123:111.222"},
		{GitHub, ProviderGitHub, `{"repository_id":123}`, `{"pull_request":42}`, "pull_request", "123#42"},
		{Discord, ProviderDiscord, `{"guild_id":"123","channel_id":"456"}`, `{"thread_id":"789"}`, "thread", "456:789"},
	} {
		t.Run(test.provider, func(t *testing.T) {
			definition, ok := Lookup(test.id)
			require.True(t, ok)
			require.Equal(t, test.provider, definition.Provider)
			canonical, err := CanonicalDestinationConfig(test.provider, json.RawMessage(test.config))
			require.NoError(t, err)
			scope, err := ResolveDestination(test.provider, canonical, json.RawMessage(test.args))
			require.NoError(t, err)
			kind, key, err := scope.Conversation()
			require.NoError(t, err)
			require.Equal(t, test.kind, kind)
			require.Equal(t, test.key, key)
			_, err = CanonicalDestinationConfig(test.provider, []byte(`{"unexpected":"secret"}`))
			require.Error(t, err)
			_, err = CanonicalDestinationConfig(test.provider, []byte(`null`))
			require.Error(t, err)
			_, err = ResolveDestination(test.provider, canonical, canonical)
			require.Error(t, err)
		})
	}
	_, ok := Lookup("external")
	require.False(t, ok)
}

func TestListenerConfigExpandsInitialAddressesAndEvents(t *testing.T) {
	for _, test := range []struct {
		id, name, raw string
		count         int
	}{
		{Slack, "thread_messages", `{"conversations":[{"channel_id":"C123","thread_ts":"1.2"},{"channel_id":"D123"}]}`, 2},
		{Discord, "thread_messages", `{"conversations":[{"channel_id":"123","thread_id":"456"}]}`, 1},
		{GitHub, "pull_request", `{"events":["commit"],"conversations":[{"repository_id":123,"pull_request":42}]}`, 1},
	} {
		t.Run(test.id, func(t *testing.T) {
			d, _ := Lookup(test.id)
			listener := d.Listeners[test.name]
			empty, err := listener.Prepare(nil)
			require.NoError(t, err)
			require.Empty(t, empty.Conversations)
			require.ElementsMatch(t, listener.Events, empty.Events)
			full, err := listener.Prepare([]byte(test.raw))
			require.NoError(t, err)
			require.Len(t, full.Conversations, test.count)
			for _, address := range full.Conversations {
				_, _, err := address.Conversation()
				require.NoError(t, err)
			}
			again, err := listener.Prepare(full.Config)
			require.NoError(t, err)
			require.Equal(t, full, again)
			for _, raw := range []string{
				`{"events":[]}`, `{"events":["bogus"]}`, `{"events":["commit","commit"]}`,
				`{"conversations":[{}]}`, `{"scope":{}}`, `null`,
			} {
				_, err := listener.Prepare([]byte(raw))
				require.Error(t, err, raw)
			}
		})
	}
}

func TestInteractionHandlersIndependentFlexibleOrFixed(t *testing.T) {
	for _, id := range []string{Slack, Discord} {
		d, _ := Lookup(id)
		require.NotNil(t, d.InteractionHandler)
		empty, err := d.InteractionHandler.Prepare(nil)
		require.NoError(t, err)
		require.Contains(t, string(empty.InputSchema), `"channel_id"`)
		channel := "C123"
		if id == Discord {
			channel = "123"
		}
		config := json.RawMessage(`{"channel_id":"` + channel + `"}`)
		fixed, err := d.InteractionHandler.Prepare(config)
		require.NoError(t, err)
		require.NotContains(t, string(fixed.InputSchema), `"channel_id"`)
		require.Contains(t, fixed.Description, channel)
		_, err = d.InteractionHandler.ResolveArgs(config, []byte(`{}`))
		require.NoError(t, err)
		_, err = d.InteractionHandler.ResolveArgs(config, config)
		require.Error(t, err)
	}
	github, _ := Lookup(GitHub)
	require.Nil(t, github.InteractionHandler)
}

func TestInvalidProviderAddresses(t *testing.T) {
	for _, scope := range []Scope{
		{}, {Slack: &SlackScope{ChannelID: "invalid"}}, {GitHub: &GitHubScope{RepositoryID: 1, PullRequest: 0}},
		{Discord: &DiscordScope{ChannelID: "0"}},
		{Slack: &SlackScope{ChannelID: "C123"}, Discord: &DiscordScope{ChannelID: "123"}},
	} {
		_, _, err := scope.Conversation()
		require.Error(t, err)
	}
	for _, config := range []string{`{"channel_id":"bad"}`, `{"channel_id":null}`, `{"thread_ts":"bad"}`} {
		_, err := CanonicalDestinationConfig(ProviderSlack, []byte(config))
		require.Error(t, err)
	}
	for _, config := range []string{
		`{"repository_id":0}`, `{"pull_request":1.1}`, `{"repository_id":9223372036854775808}`,
	} {
		_, err := CanonicalDestinationConfig(ProviderGitHub, []byte(config))
		require.Error(t, err)
	}
}

func TestOriginArgumentsRespectFixedHandlerDestination(t *testing.T) {
	definition, _ := Lookup(Slack)
	handler := definition.InteractionHandler
	origin := Scope{Slack: &SlackScope{ChannelID: "C123", ThreadTS: "1.2"}}
	for _, config := range []string{`{}`, `{"channel_id":"C123"}`, `{"channel_id":"C123","thread_ts":"1.2"}`} {
		args, err := handler.ArgsForDestination([]byte(config), origin)
		require.NoError(t, err)
		concrete, err := handler.ResolveArgs([]byte(config), args)
		require.NoError(t, err)
		require.Equal(t, origin, concrete)
	}
	_, err := handler.ArgsForDestination([]byte(`{"channel_id":"C456"}`), origin)
	require.Error(t, err)
	_, err = handler.ArgsForDestination([]byte(`{"channel_id":"C123","thread_ts":"1.3"}`), origin)
	require.Error(t, err)
	_, err = handler.ArgsForDestination(nil, Scope{Discord: &DiscordScope{ChannelID: "123"}})
	require.Error(t, err)
}

func TestFixedDestinationRequiresParent(t *testing.T) {
	for _, test := range []struct {
		provider string
		config   any
		parent   string
	}{
		{ProviderSlack, SlackConfig{ThreadTS: "111.222"}, "channel_id"},
		{ProviderGitHub, GitHubConfig{PullRequest: 42}, "repository_id"},
		{ProviderDiscord, DiscordConfig{ThreadID: "789"}, "channel_id"},
		{ProviderDiscord, DiscordConfig{GuildID: "123", ThreadID: "789"}, "channel_id"},
	} {
		t.Run(test.provider, func(t *testing.T) {
			raw, err := json.Marshal(test.config)
			require.NoError(t, err)
			schema, err := DestinationConfigSchema(test.provider)
			require.NoError(t, err)
			require.ErrorContains(t, jsonschema.Validate(schema, raw), test.parent)
			_, err = CanonicalDestinationConfig(test.provider, raw)
			require.ErrorContains(t, err, test.parent)
			_, _, err = DestinationArguments(test.provider, raw)
			require.Error(t, err)
		})
	}
}

func TestPartialFixedDestinationsPreserveProviderOptions(t *testing.T) {
	for _, test := range []struct {
		name, provider string
		config         any
		args           string
		want           Scope
	}{
		{"slack_channel", ProviderSlack, SlackConfig{ChannelID: "C123"}, `{"thread_ts":"111.222"}`,
			Scope{Slack: &SlackScope{ChannelID: "C123", ThreadTS: "111.222"}}},
		{"github_repository", ProviderGitHub, GitHubConfig{RepositoryID: 123}, `{"pull_request":42}`,
			Scope{GitHub: &GitHubScope{RepositoryID: 123, PullRequest: 42}}},
		{"discord_guild", ProviderDiscord, DiscordConfig{GuildID: "123"}, `{"channel_id":"456","thread_id":"789"}`,
			Scope{Discord: &DiscordScope{GuildID: "123", ChannelID: "456", ThreadID: "789"}}},
		{"discord_channel_without_guild", ProviderDiscord, DiscordConfig{ChannelID: "456"}, `{"thread_id":"789"}`,
			Scope{Discord: &DiscordScope{ChannelID: "456", ThreadID: "789"}}},
		{"discord_thread_without_guild", ProviderDiscord, DiscordConfig{ChannelID: "456", ThreadID: "789"}, `{}`,
			Scope{Discord: &DiscordScope{ChannelID: "456", ThreadID: "789"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := json.Marshal(test.config)
			require.NoError(t, err)
			canonical, err := CanonicalDestinationConfig(test.provider, raw)
			require.NoError(t, err)
			resolved, err := ResolveDestination(test.provider, canonical, json.RawMessage(test.args))
			require.NoError(t, err)
			require.Equal(t, test.want, resolved)
		})
	}
}
