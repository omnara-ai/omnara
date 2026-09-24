package integrationdefinition

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/omnara-ai/omnara/internal/jsonschema"
	"github.com/stretchr/testify/require"
)

func TestRegistryAndTypedDestinations(t *testing.T) {
	for _, test := range []struct {
		integrationType           Type
		provider, args, kind, key string
	}{
		{SlackThread, ProviderSlack, `{"channel_id":"C123","thread_ts":"111.222"}`, "thread", "C123:111.222"},
		{GitHubPR, ProviderGitHub, `{"repository_id":9007199254740993,"pull_request":42}`,
			"pull_request", "9007199254740993#42"},
		{DiscordThread, ProviderDiscord, `{"guild_id":"123","channel_id":"456","thread_id":"789"}`, "thread", "456:789"},
	} {
		t.Run(test.provider, func(t *testing.T) {
			definition, ok := Lookup(test.integrationType)
			require.True(t, ok)
			require.Equal(t, test.integrationType, definition.IntegrationType)
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
}

func TestIntegrationTypesPartitionRegisteredTransports(t *testing.T) {
	seen := map[Type]bool{}
	for _, provider := range []string{ProviderSlack, ProviderDiscord, ProviderGitHub} {
		integrationTypes := IntegrationTypesForProvider(provider)
		require.NotEmpty(t, integrationTypes)
		for _, value := range integrationTypes {
			integrationType := Type(value)
			require.False(t, seen[integrationType], "each integration type belongs to exactly one transport")
			seen[integrationType] = true
			definition, ok := Lookup(integrationType)
			require.True(t, ok)
			require.Equal(t, integrationType, definition.IntegrationType)
			require.Equal(t, provider, definition.Provider)
			require.Equal(t, provider, ProviderForType(integrationType))
		}
	}
	require.Len(t, seen, len(All()))
	for _, definition := range All() {
		require.True(t, seen[definition.IntegrationType], "registered types must be discoverable")
	}
	for _, unknown := range []Type{"", "slack", "slack_unregistered", "discord_unregistered", "github_unregistered"} {
		_, ok := Lookup(unknown)
		require.False(t, ok)
		require.Empty(t, ProviderForType(unknown), "a transport prefix grants no authority")
	}
	for _, unknown := range []string{"", "unknown", "slack_thread", "discord_thread", "github_pr"} {
		require.Empty(t, IntegrationTypesForProvider(unknown), "discovery accepts transport names only")
	}
}

func TestIntegrationLaunchDeclarations(t *testing.T) {
	events := []Event{
		{Scope: Scope{Slack: &SlackScope{ChannelID: "C123"}}, Kind: "message", Mentioned: true},
		{
			Scope: Scope{Discord: &DiscordScope{GuildID: "123", ChannelID: "456"}},
			Kind:  "message", Mentioned: true,
		},
		{
			Scope: Scope{GitHub: &GitHubScope{RepositoryID: 123, PullRequest: 7}},
			Kind:  "discussion_comment", Mentioned: true,
		},
		{Scope: Scope{GitHub: &GitHubScope{RepositoryID: 123, PullRequest: 7}}, Kind: "pull_request_opened"},
	}
	for _, definition := range All() {
		t.Run(string(definition.IntegrationType), func(t *testing.T) {
			if definition.SubscribeOnLaunch {
				require.NotNil(t, definition.Subscription)
			}
			for _, trigger := range definition.LaunchTriggers {
				require.True(t, slices.ContainsFunc(events, func(event Event) bool {
					return definition.MatchesLauncher(event, trigger)
				}), "declared trigger %q must match a supported provider event", trigger)
			}
		})
	}
}

func TestSubscriptionsPrepareConcreteConversation(t *testing.T) {
	for _, test := range []struct {
		integrationType         Type
		conversation, kind, ref string
	}{
		{SlackThread, `{"channel_id":"C123","thread_ts":"1.2"}`, "thread", "C123:1.2"},
		{SlackThread, `{"channel_id":"D123"}`, "dm", "D123"},
		{SlackThread, `{"channel_id":"C123"}`, "channel", "C123"},
		{DiscordThread, `{"channel_id":"123","thread_id":"456"}`, "thread", "123:456"},
		{DiscordThread, `{"channel_id":"123"}`, "channel", "123"},
		{GitHubPR, `{"repository_id":123,"pull_request":42}`, "pull_request", "123#42"},
	} {
		t.Run(string(test.integrationType)+"/"+test.kind, func(t *testing.T) {
			d, _ := Lookup(test.integrationType)
			subscription := d.Subscription
			schema, err := subscription.ConversationSchema()
			require.NoError(t, err)
			require.NoError(t, jsonschema.Validate(schema, []byte(test.conversation)))
			prepared, err := subscription.Prepare([]byte(test.conversation))
			require.NoError(t, err)
			kind, ref, err := prepared.Conversation()
			require.NoError(t, err)
			require.Equal(t, test.kind, kind)
			require.Equal(t, test.ref, ref)
			parsed, err := ParseConversation(d.Provider, kind, ref)
			require.NoError(t, err)
			raw, err := parsed.ConversationJSON()
			require.NoError(t, err)
			require.JSONEq(t, test.conversation, string(raw))
			again, err := subscription.Prepare(raw)
			require.NoError(t, err)
			require.Equal(t, prepared, again)
			for _, raw := range []string{
				`{}`, `null`, `[]`, `{"conversations":[]}`, `{"slack":{"channel_id":"C123"}}`,
				`{"channel_id":"C123","events":["message"]}`, `{"channel_id":"bad"}`,
				`{"channel_id":"C123","thread_ts":""}`, `{"repository_id":0,"pull_request":1}`,
				`{"repository_id":9223372036854775808,"pull_request":1}`,
				`{"channel_id":"123","thread_id":"123456789012345678901"}`,
			} {
				_, err := subscription.Prepare([]byte(raw))
				require.Error(t, err, raw)
			}
			_, err = subscription.Prepare(nil)
			require.Error(t, err)
		})
	}
}

func TestIntegrationForwardingPolicy(t *testing.T) {
	for _, definition := range All() {
		t.Run(string(definition.IntegrationType), func(t *testing.T) {
			for _, event := range []string{
				"message", "discussion_comment", "review_comment", "commit", "pull_request_opened", "unknown",
			} {
				want := event == "message"
				if definition.IntegrationType == GitHubPR {
					want = event == "discussion_comment" || event == "review_comment" || event == "commit"
				}
				require.Equal(t, want, definition.Forwards(event), event)
			}
		})
	}
	require.False(t, (Definition{}).Forwards("message"))
	_, err := (SubscriptionDefinition{Provider: "unknown"}).ConversationSchema()
	require.Error(t, err)
	_, err = (SubscriptionDefinition{Provider: "unknown"}).Prepare([]byte(`{"channel_id":"C123"}`))
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

func TestInteractionHandlersUseAssignedConversation(t *testing.T) {
	for _, test := range []struct {
		integrationType Type
		kind            string
		ref             string
	}{{SlackThread, "thread", "C123:1.2"}, {SlackThread, "dm", "D123"}, {DiscordThread, "thread", "123:456"}} {
		t.Run(string(test.integrationType), func(t *testing.T) {
			d, _ := Lookup(test.integrationType)
			prepared, err := d.InteractionHandler.Prepare()
			require.NoError(t, err)
			require.NoError(t, jsonschema.Validate(prepared.InputSchema, []byte(`{}`)))
			scope, err := ParseConversation(d.Provider, test.kind, test.ref)
			require.NoError(t, err)
			kind, ref, err := scope.Conversation()
			require.NoError(t, err)
			require.Equal(t, test.kind, kind)
			require.Equal(t, test.ref, ref)
			for _, args := range []string{`{"channel_id":"C123"}`, `{"guild_id":"789"}`, `null`, `[]`} {
				err = d.InteractionHandler.ValidateArgs([]byte(args))
				require.Error(t, err)
			}
			_, err = ParseConversation(d.Provider, "thread", "invalid")
			require.Error(t, err)
		})
	}
	github, _ := Lookup(GitHubPR)
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
	for _, config := range []string{
		`{"channel_id":"bad"}`,
		`{"channel_id":null}`,
		`{"channel_id":"C123","thread_ts":"bad"}`,
	} {
		_, err := ResolveDestination(ProviderSlack, []byte(config))
		require.Error(t, err)
	}
	for _, config := range []string{
		`{"repository_id":0,"pull_request":1}`, `{"repository_id":1,"pull_request":1.1}`,
		`{"repository_id":9223372036854775808,"pull_request":1}`,
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
