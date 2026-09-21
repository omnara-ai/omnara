package appdefinition

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/omnara-ai/omnara/internal/jsonschema"
	"github.com/stretchr/testify/require"
)

func TestRegistryAndTypedDestinations(t *testing.T) {
	for _, test := range []struct{ id, provider, args, kind, key string }{
		{Slack, ProviderSlack, `{"channel_id":"C123","thread_ts":"111.222"}`, "thread", "C123:111.222"},
		{GitHub, ProviderGitHub, `{"repository_id":9007199254740993,"pull_request":42}`,
			"pull_request", "9007199254740993#42"},
		{Discord, ProviderDiscord, `{"guild_id":"123","channel_id":"456","thread_id":"789"}`, "thread", "456:789"},
	} {
		t.Run(test.provider, func(t *testing.T) {
			definition, ok := Lookup(test.id)
			require.True(t, ok)
			require.Equal(t, test.provider, definition.Provider)
			scope, err := ResolveDestination(test.provider, json.RawMessage(test.args))
			require.NoError(t, err)
			kind, key, err := scope.Conversation()
			require.NoError(t, err)
			require.Equal(t, test.kind, kind)
			require.Equal(t, test.key, key)
			for _, invalid := range []string{`{"unexpected":"secret"}`, `null`, `{}`, `[]`} {
				_, err = ResolveDestination(test.provider, []byte(invalid))
				require.Error(t, err)
			}
		})
	}
	_, ok := Lookup("external")
	require.False(t, ok)
}

func TestSubscriptionsPrepareConcreteConversationAndEvents(t *testing.T) {
	for _, test := range []struct {
		id, name, conversation, kind, ref string
	}{
		{Slack, "thread_messages", `{"channel_id":"C123","thread_ts":"1.2"}`, "thread", "C123:1.2"},
		{Slack, "thread_messages", `{"channel_id":"D123"}`, "dm", "D123"},
		{Slack, "thread_messages", `{"channel_id":"C123"}`, "channel", "C123"},
		{Discord, "thread_messages", `{"channel_id":"123","thread_id":"456"}`, "thread", "123:456"},
		{Discord, "thread_messages", `{"channel_id":"123"}`, "channel", "123"},
		{GitHub, "pull_request", `{"repository_id":123,"pull_request":42}`, "pull_request", "123#42"},
	} {
		t.Run(test.id+"/"+test.kind, func(t *testing.T) {
			d, _ := Lookup(test.id)
			subscription := d.Subscriptions[test.name]
			schema, err := subscription.ConversationSchema()
			require.NoError(t, err)
			require.NoError(t, jsonschema.Validate(schema, []byte(test.conversation)))
			prepared, err := subscription.Prepare([]byte(test.conversation), nil)
			require.NoError(t, err)
			require.ElementsMatch(t, subscription.Events, prepared.Events)
			require.True(t, slices.IsSorted(prepared.Events))
			kind, ref, err := prepared.Scope.Conversation()
			require.NoError(t, err)
			require.Equal(t, test.kind, kind)
			require.Equal(t, test.ref, ref)
			parsed, err := ParseConversation(d.Provider, kind, ref)
			require.NoError(t, err)
			raw, err := parsed.ConversationJSON()
			require.NoError(t, err)
			require.JSONEq(t, test.conversation, string(raw))
			again, err := subscription.Prepare(raw, prepared.Events)
			require.NoError(t, err)
			require.Equal(t, prepared, again)
			for _, raw := range []string{
				`{}`, `null`, `[]`, `{"conversations":[]}`, `{"slack":{"channel_id":"C123"}}`,
				`{"channel_id":"C123","events":["message"]}`, `{"channel_id":"bad"}`,
				`{"channel_id":"C123","thread_ts":""}`, `{"repository_id":0,"pull_request":1}`,
				`{"repository_id":9223372036854775808,"pull_request":1}`,
				`{"channel_id":"123","thread_id":"123456789012345678901"}`,
			} {
				_, err := subscription.Prepare([]byte(raw), nil)
				require.Error(t, err, raw)
			}
			_, err = subscription.Prepare(nil, nil)
			require.Error(t, err)
			for _, events := range [][]string{{}, {"bogus"}, {subscription.Events[0], subscription.Events[0]}} {
				_, err := subscription.Prepare([]byte(test.conversation), events)
				require.Error(t, err, events)
			}
		})
	}
}

func TestSubscriptionEventSelectionIsCanonicalAndOwned(t *testing.T) {
	d, _ := Lookup(GitHub)
	subscription := d.Subscriptions["pull_request"]
	events := []string{"review_comment", "commit"}
	prepared, err := subscription.Prepare([]byte(`{"repository_id":123,"pull_request":42}`), events)
	require.NoError(t, err)
	require.Equal(t, []string{"commit", "review_comment"}, prepared.Events)
	require.Equal(t, []string{"review_comment", "commit"}, events)
	prepared.Events[0] = "changed"
	require.Equal(t, "commit", events[1])
	prepared, err = subscription.Prepare([]byte(`{"repository_id":123,"pull_request":42}`), nil)
	require.NoError(t, err)
	prepared.Events[0] = "changed"
	require.NotContains(t, subscription.Events, "changed")
	_, err = (SubscriptionDefinition{Provider: "unknown"}).ConversationSchema()
	require.Error(t, err)
	_, err = (SubscriptionDefinition{Provider: "unknown"}).Prepare([]byte(`{"channel_id":"C123"}`), nil)
	require.Error(t, err)
}

func TestParseConversationRejectsParentAndMismatchedAddresses(t *testing.T) {
	for _, test := range []struct{ provider, kind, ref string }{
		{ProviderSlack, "workspace", "T123"}, {ProviderSlack, "channel", "D123"},
		{ProviderSlack, "dm", "C123"}, {ProviderSlack, "thread", "C123:"},
		{ProviderSlack, "thread", "C123:1.2:3.4"}, {ProviderSlack, "channel", "C123:1.2"},
		{ProviderDiscord, "guild", "123"}, {ProviderDiscord, "thread", "123:"},
		{ProviderDiscord, "dm", "123"}, {ProviderDiscord, "thread", "123:0"},
		{ProviderGitHub, "repository", "123"}, {ProviderGitHub, "installation", "123"},
		{ProviderGitHub, "pull_request", "0#1"}, {ProviderGitHub, "pull_request", "1#0"},
		{ProviderGitHub, "pull_request", "123"}, {ProviderGitHub, "pull_request", "1#1#1"},
		{ProviderGitHub, "pull_request", "9223372036854775808#1"}, {"unknown", "channel", "C123"},
	} {
		_, err := ParseConversation(test.provider, test.kind, test.ref)
		require.Error(t, err, test)
	}
	scope, err := ParseConversation(ProviderGitHub, " pull_request ", " 00123#0042 ")
	require.NoError(t, err)
	kind, ref, err := scope.Conversation()
	require.NoError(t, err)
	require.Equal(t, "pull_request", kind)
	require.Equal(t, "123#42", ref)
	_, err = (Scope{}).ConversationJSON()
	require.Error(t, err)
	_, err = (Scope{Slack: &SlackScope{ChannelID: "C123"}, Discord: &DiscordScope{ChannelID: "123"}}).ConversationJSON()
	require.Error(t, err)
	withGuild := Scope{Discord: &DiscordScope{GuildID: "111", ChannelID: "123", ThreadID: "456"}}
	raw, err := withGuild.ConversationJSON()
	require.NoError(t, err)
	require.JSONEq(t, `{"guild_id":"111","channel_id":"123","thread_id":"456"}`, string(raw))
}

func TestInteractionHandlersRequireCompleteIndependentDestinations(t *testing.T) {
	for _, test := range []struct{ id, args string }{
		{Slack, `{"channel_id":"C123","thread_ts":"1.2"}`},
		{Slack, `{"channel_id":"C456"}`},
		{Discord, `{"channel_id":"123","thread_id":"456"}`},
		{Discord, `{"guild_id":"789","channel_id":"123"}`},
	} {
		t.Run(test.id+test.args, func(t *testing.T) {
			d, _ := Lookup(test.id)
			prepared, err := d.InteractionHandler.Prepare()
			require.NoError(t, err)
			require.NoError(t, jsonschema.Validate(prepared.InputSchema, []byte(test.args)))
			require.Error(t, jsonschema.Validate(prepared.InputSchema, []byte(`{}`)))
			_, err = d.InteractionHandler.ResolveArgs([]byte(`{}`))
			require.Error(t, err)
			destination, err := d.InteractionHandler.ResolveArgs([]byte(test.args))
			require.NoError(t, err)
			// Verified input origins carry all handler arguments, including threads.
			origin, err := destination.ConversationJSON()
			require.NoError(t, err)
			require.JSONEq(t, test.args, string(origin))
			resolved, err := d.InteractionHandler.ResolveArgs(origin)
			require.NoError(t, err)
			require.Equal(t, destination, resolved)
		})
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
		_, err := ResolveDestination(ProviderSlack, []byte(config))
		require.Error(t, err)
	}
	for _, config := range []string{
		`{"repository_id":0}`, `{"pull_request":1.1}`, `{"repository_id":9223372036854775808}`,
	} {
		_, err := ResolveDestination(ProviderGitHub, []byte(config))
		require.Error(t, err)
	}
}

func TestDestinationsRequireCompleteAddress(t *testing.T) {
	for _, test := range []struct{ provider, args, missing string }{
		{ProviderSlack, `{"thread_ts":"111.222"}`, "channel_id"},
		{ProviderGitHub, `{"pull_request":42}`, "repository_id"},
		{ProviderGitHub, `{"repository_id":123}`, "pull_request"},
		{ProviderDiscord, `{"thread_id":"789"}`, "channel_id"},
		{ProviderDiscord, `{"guild_id":"123"}`, "channel_id"},
	} {
		_, err := ResolveDestination(test.provider, []byte(test.args))
		require.ErrorContains(t, err, test.missing)
	}
}
