package appdefinition

import "testing"

func TestScopeConversationAndContainment(t *testing.T) {
	channel := Scope{Slack: &SlackScope{ChannelID: "C123"}}
	thread := Scope{Slack: &SlackScope{ChannelID: "C123", ThreadTS: "111.222"}}
	otherThread := Scope{Slack: &SlackScope{ChannelID: "C123", ThreadTS: "111.333"}}
	pr := Scope{GitHub: &GitHubScope{RepositoryID: 123, PullRequest: 42}}
	discord := Scope{Discord: &DiscordScope{GuildID: "123", ChannelID: "456"}}
	discordThread := Scope{Discord: &DiscordScope{GuildID: "123", ChannelID: "456", ThreadID: "789"}}
	for _, test := range []struct {
		scope     Scope
		kind, key string
	}{
		{channel, "channel", "C123"}, {thread, "thread", "C123:111.222"},
		{Scope{Slack: &SlackScope{ChannelID: "D123"}}, "dm", "D123"},
		{pr, "pull_request", "123#42"}, {discord, "channel", "456"}, {discordThread, "thread", "456:789"},
	} {
		kind, key, err := test.scope.Conversation()
		if err != nil || kind != test.kind || key != test.key {
			t.Errorf("conversation = (%q,%q,%v), want (%q,%q)", kind, key, err, test.kind, test.key)
		}
		if !test.scope.Contains(test.scope) {
			t.Errorf("scope does not contain itself: %+v", test.scope)
		}
	}
	for _, test := range []struct {
		parent, child Scope
		want          bool
	}{
		{channel, thread, true}, {thread, channel, false}, {thread, otherThread, false},
		{channel, Scope{Slack: &SlackScope{ChannelID: "C456"}}, false},
		{channel, pr, false}, {pr, Scope{GitHub: &GitHubScope{RepositoryID: 123, PullRequest: 42}}, true},
		{pr, Scope{GitHub: &GitHubScope{RepositoryID: 123, PullRequest: 43}}, false},
		{pr, Scope{GitHub: &GitHubScope{RepositoryID: 456, PullRequest: 42}}, false},
		{discord, discordThread, true}, {discordThread, discord, false},
		{discord, Scope{Discord: &DiscordScope{GuildID: "999", ChannelID: "456", ThreadID: "789"}}, false},
	} {
		if got := test.parent.Contains(test.child); got != test.want {
			t.Errorf("Contains(%+v, %+v) = %v, want %v", test.parent, test.child, got, test.want)
		}
	}
}

func TestInvalidScopesFailClosed(t *testing.T) {
	for _, scope := range []Scope{
		{}, {Slack: &SlackScope{ChannelID: "C123"}, Discord: &DiscordScope{ChannelID: "123"}},
		{Slack: &SlackScope{ChannelID: "${channel}"}}, {Slack: &SlackScope{ChannelID: "C123", ThreadTS: "${thread}"}},
		{GitHub: &GitHubScope{RepositoryID: 123}},
		{GitHub: &GitHubScope{RepositoryID: 0, PullRequest: 42}},
		{Discord: &DiscordScope{ChannelID: "0"}},
	} {
		if _, _, err := scope.Conversation(); err == nil {
			t.Errorf("accepted invalid scope: %+v", scope)
		}
		if scope.Contains(scope) {
			t.Errorf("invalid scope contains itself: %+v", scope)
		}
	}
}

func TestCapabilitiesValidateOnlySelectedProviderFeatures(t *testing.T) {
	github, _ := Lookup(GitHub)
	scope := &Scope{GitHub: &GitHubScope{RepositoryID: 123, PullRequest: 42}}
	if err := github.ValidateCapabilities(
		scope,
		&Listener{Events: []string{"commit", "discussion_comment"}},
		nil,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if err := github.ValidateCapabilities(
		scope,
		nil,
		nil,
		&InteractionHandler{Definition: SlackInteractions},
	); err == nil {
		t.Fatal("GitHub accepted a Slack interaction handler")
	}
	for _, listener := range []*Listener{{}, {Events: []string{"message"}}, {Events: []string{"commit", "commit"}}} {
		if err := github.ValidateCapabilities(scope, listener, nil, nil); err == nil {
			t.Fatalf("accepted invalid listener: %+v", listener)
		}
	}
	slack, _ := Lookup(Slack)
	if err := slack.ValidateCapabilities(nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := slack.ValidateCapabilities(
		nil,
		nil,
		nil,
		&InteractionHandler{Definition: SlackInteractions},
	); err == nil {
		t.Fatal("handler accepted without scope")
	}
}
