package appdefinition

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ConversationJSON encodes one flat provider address, without a provider wrapper.
func (s Scope) ConversationJSON() (json.RawMessage, error) {
	if err := s.Validate(s.Provider()); err != nil {
		return nil, err
	}
	switch s.Provider() {
	case ProviderSlack:
		return json.Marshal(s.Slack)
	case ProviderGitHub:
		return json.Marshal(s.GitHub)
	case ProviderDiscord:
		return json.Marshal(s.Discord)
	default:
		return nil, fmt.Errorf("unsupported conversation provider")
	}
}

// ParseConversation reconstructs a concrete address from the indexed routing
// key. Discord guild IDs are optional metadata and are not part of that key.
// Parent launcher scopes (workspace, repository, installation) are not
// conversations. Numeric GitHub references are canonicalized by Conversation.
func ParseConversation(provider, kind, ref string) (Scope, error) {
	kind, ref = strings.TrimSpace(kind), strings.TrimSpace(ref)
	invalid := func() (Scope, error) {
		return Scope{}, fmt.Errorf("invalid %s conversation %q", provider, kind)
	}
	var scope Scope
	switch provider {
	case ProviderSlack:
		switch kind {
		case "channel", "dm":
			scope.Slack = &SlackScope{ChannelID: ref}
		case "thread":
			channel, timestamp, ok := strings.Cut(ref, ":")
			if !ok || timestamp == "" {
				return invalid()
			}
			scope.Slack = &SlackScope{ChannelID: channel, ThreadTS: timestamp}
		default:
			return invalid()
		}
	case ProviderGitHub:
		if kind != "pull_request" {
			return invalid()
		}
		repository, rawNumber, ok := strings.Cut(ref, "#")
		if !ok {
			return invalid()
		}
		repositoryID, err := strconv.ParseInt(repository, 10, 64)
		if err != nil {
			return invalid()
		}
		number, err := strconv.Atoi(rawNumber)
		if err != nil {
			return invalid()
		}
		scope.GitHub = &GitHubScope{RepositoryID: repositoryID, PullRequest: number}
	case ProviderDiscord:
		switch kind {
		case "channel":
			scope.Discord = &DiscordScope{ChannelID: ref}
		case "thread":
			channel, thread, ok := strings.Cut(ref, ":")
			if !ok || thread == "" {
				return invalid()
			}
			scope.Discord = &DiscordScope{ChannelID: channel, ThreadID: thread}
		default:
			return invalid()
		}
	default:
		return invalid()
	}
	canonicalKind, _, err := scope.Conversation()
	if err != nil || canonicalKind != kind {
		return invalid()
	}
	return scope, nil
}
