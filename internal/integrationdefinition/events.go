package integrationdefinition

import (
	"fmt"
	"slices"
	"strconv"
)

type Event struct {
	Scope     Scope  `json:"scope"`
	Kind      string `json:"kind"`
	Mentioned bool   `json:"mentioned,omitempty"`
}

type EventAddress struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

func (e Event) Validate() error {
	if err := e.Scope.Validate(e.Scope.Provider()); err != nil {
		return err
	}
	switch e.Scope.Provider() {
	case ProviderSlack, ProviderDiscord:
		if e.Kind == "message" {
			return nil
		}
	case ProviderGitHub:
		switch e.Kind {
		case "discussion_comment", "review_comment", "commit", "pull_request_opened":
			return nil
		}
	}
	return fmt.Errorf("unsupported hosted integration event %q", e.Kind)
}

func (d Definition) SupportsLaunchTrigger(trigger string) bool {
	return slices.Contains(d.LaunchTriggers, trigger)
}

func (d Definition) MatchesLauncher(e Event, trigger string) bool {
	if !d.SupportsLaunchTrigger(trigger) || e.Scope.Provider() != d.Provider || e.Validate() != nil {
		return false
	}
	if e.Scope.Discord != nil && e.Scope.Discord.GuildID == "" {
		return false
	}
	switch trigger {
	case "mention":
		return e.Mentioned && (e.Kind == "message" || e.Kind == "discussion_comment" || e.Kind == "review_comment")
	case "pull_request_opened":
		return e.Scope.GitHub != nil && e.Kind == "pull_request_opened"
	default:
		return false
	}
}

func (e Event) RoutingAddresses(account string) ([]EventAddress, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	kind, ref, _ := e.Scope.Conversation()
	addresses := []EventAddress{{kind, ref}}
	add := func(kind, ref string) error {
		k, r, err := CanonicalLauncherScope(e.Scope.Provider(), kind, ref)
		if err == nil {
			addresses = append(addresses, EventAddress{k, r})
		}
		return err
	}
	switch {
	case e.Scope.Slack != nil:
		if e.Scope.Slack.ThreadTS != "" {
			parent := Scope{Slack: &SlackScope{ChannelID: e.Scope.Slack.ChannelID}}
			k, r, _ := parent.Conversation()
			addresses = append(addresses, EventAddress{k, r})
		}
		if err := add("workspace", account); err != nil {
			return nil, err
		}
	case e.Scope.GitHub != nil:
		if err := add("repository", strconv.FormatInt(e.Scope.GitHub.RepositoryID, 10)); err != nil {
			return nil, err
		}
		if err := add("installation", account); err != nil {
			return nil, err
		}
	case e.Scope.Discord != nil:
		if e.Scope.Discord.ThreadID != "" {
			parent := Scope{Discord: &DiscordScope{ChannelID: e.Scope.Discord.ChannelID}}
			k, r, _ := parent.Conversation()
			addresses = append(addresses, EventAddress{k, r})
		}
	}
	return addresses, nil
}
