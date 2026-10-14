package integrationdefinition

import (
	"fmt"
	"slices"
)

type EventKind string

const (
	EventMessage           EventKind = "message"
	EventDiscussionComment EventKind = "discussion_comment"
	EventReviewComment     EventKind = "review_comment"
	EventCommit            EventKind = "commit"
	EventPullRequestOpened EventKind = "pull_request_opened"
)

type Event struct {
	Scope     Scope     `json:"scope"`
	Kind      EventKind `json:"kind"`
	Mentioned bool      `json:"mentioned,omitempty"`
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
		if e.Kind == EventMessage {
			return nil
		}
	case ProviderGitHub:
		switch e.Kind {
		case EventDiscussionComment, EventReviewComment, EventCommit, EventPullRequestOpened:
			return nil
		default:
			return fmt.Errorf("unsupported hosted integration event %q", e.Kind)
		}
	}
	return fmt.Errorf("unsupported hosted integration event %q", e.Kind)
}

func (d Definition) Forwards(event EventKind) bool {
	return d.Subscription != nil && slices.Contains(d.Subscription.Events, event)
}

func (e Event) RoutingAddresses() ([]EventAddress, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	kind, ref, _ := e.Scope.Conversation()
	addresses := []EventAddress{{kind, ref}}
	switch {
	case e.Scope.Slack != nil:
		if e.Scope.Slack.ThreadTS != "" {
			parent := Scope{Slack: &SlackScope{ChannelID: e.Scope.Slack.ChannelID}}
			k, r, _ := parent.Conversation()
			addresses = append(addresses, EventAddress{k, r})
		}
	case e.Scope.Discord != nil:
		if e.Scope.Discord.ThreadID != "" && e.Scope.Discord.ChannelID != "" {
			parent := Scope{Discord: &DiscordScope{ChannelID: e.Scope.Discord.ChannelID}}
			k, r, _ := parent.Conversation()
			addresses = append(addresses, EventAddress{k, r})
		}
	}
	return addresses, nil
}
