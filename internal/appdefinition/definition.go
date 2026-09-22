// Package appdefinition describes app tools, subscriptions and interaction
// handlers without depending on storage, provider transports or the agent compiler.
// Launchers and credentials belong to project setup.
package appdefinition

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type Type string

const (
	ProviderSlack        = "slack"
	ProviderGitHub       = "github"
	ProviderDiscord      = "discord"
	SlackThread     Type = "slack_thread"
	GitHubPR        Type = "github_pr"
	DiscordThread   Type = "discord_thread"
)

// Definition is the reviewed capability registry for one immutable app kind.
type Definition struct {
	AppType            Type
	Provider           string
	Tools              []string
	Subscriptions      map[string]SubscriptionDefinition
	InteractionHandler *InteractionHandlerDefinition
	Schedule           *ScheduleDefinition
}

// All returns the installed app implementations in stable catalog order.
func All() []Definition {
	definitions := make([]Definition, 0, 3)
	for _, id := range []Type{DiscordThread, GitHubPR, SlackThread} {
		definition, _ := Lookup(id)
		definitions = append(definitions, definition)
	}
	return definitions
}

func Lookup(id Type) (Definition, bool) {
	var d Definition
	switch id {
	case SlackThread:
		d = Definition{
			AppType:  id,
			Provider: ProviderSlack,
			Tools:    []string{"read", "post_message"},
			Subscriptions: map[string]SubscriptionDefinition{
				"thread_messages": {Name: "thread_messages", Provider: ProviderSlack, Events: []string{"message"}},
			},
			InteractionHandler: &InteractionHandlerDefinition{Provider: ProviderSlack},
			Schedule:           slackThreadSchedule,
		}
	case DiscordThread:
		d = Definition{
			AppType:  id,
			Provider: ProviderDiscord,
			Tools:    []string{"read", "post_message"},
			Subscriptions: map[string]SubscriptionDefinition{
				"thread_messages": {Name: "thread_messages", Provider: ProviderDiscord, Events: []string{"message"}},
			},
			InteractionHandler: &InteractionHandlerDefinition{Provider: ProviderDiscord},
			Schedule:           discordThreadSchedule,
		}
	case GitHubPR:
		d = Definition{
			AppType:  id,
			Provider: ProviderGitHub,
			Tools:    []string{"read", "discussion_comment", "inline_comment", "reply"},
			Subscriptions: map[string]SubscriptionDefinition{
				"pull_request": {
					Name:     "pull_request",
					Provider: ProviderGitHub,
					Events:   []string{"discussion_comment", "review_comment", "commit"},
				},
			},
		}
	default:
		return Definition{}, false
	}
	return d, true
}

// AppTypesForProvider supplies indexed discovery with the types that share a
// transport. The association comes from registration, never the type's spelling.
func AppTypesForProvider(provider string) []string {
	var types []string
	for _, definition := range All() {
		if definition.Provider == provider {
			types = append(types, string(definition.AppType))
		}
	}
	return types
}

// ProviderForType returns the registered transport, or empty for an unknown type.
func ProviderForType(appType Type) string {
	definition, _ := Lookup(appType)
	return definition.Provider
}

// Scope contains exactly one provider-specific, concrete address. Optional
// thread fields select a whole channel when omitted; they are not placeholders.
type Scope struct {
	Slack   *SlackScope   `json:"slack,omitempty"`
	GitHub  *GitHubScope  `json:"github,omitempty"`
	Discord *DiscordScope `json:"discord,omitempty"`
}

type SlackScope struct {
	ChannelID string `json:"channel_id"`
	ThreadTS  string `json:"thread_ts,omitempty"`
}

type GitHubScope struct {
	RepositoryID int64 `json:"repository_id"`
	PullRequest  int   `json:"pull_request"`
}

type DiscordScope struct {
	GuildID   string `json:"guild_id,omitempty"`
	ChannelID string `json:"channel_id"`
	ThreadID  string `json:"thread_id,omitempty"`
}

var (
	slackChannel   = regexp.MustCompile(`^[CDG][A-Z0-9]+$`)
	slackTimestamp = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)
	discordID      = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
)

func (s Scope) Validate(provider string) error {
	count := 0
	for _, present := range []bool{s.Slack != nil, s.GitHub != nil, s.Discord != nil} {
		if present {
			count++
		}
	}
	if count != 1 {
		return fmt.Errorf("scope must contain exactly one provider address")
	}
	switch provider {
	case ProviderSlack:
		if s.Slack == nil {
			break
		}
		if !slackChannel.MatchString(s.Slack.ChannelID) ||
			(s.Slack.ThreadTS != "" && !slackTimestamp.MatchString(s.Slack.ThreadTS)) {
			return fmt.Errorf("slack scope requires a channel_id and, when supplied, a concrete thread_ts")
		}
		return nil
	case ProviderGitHub:
		if s.GitHub == nil {
			break
		}
		if s.GitHub.RepositoryID <= 0 || s.GitHub.PullRequest <= 0 {
			return fmt.Errorf("github scope requires a positive repository_id and pull_request")
		}
		return nil
	case ProviderDiscord:
		if s.Discord == nil {
			break
		}
		if !discordID.MatchString(s.Discord.ChannelID) ||
			(s.Discord.GuildID != "" && !discordID.MatchString(s.Discord.GuildID)) ||
			(s.Discord.ThreadID != "" && !discordID.MatchString(s.Discord.ThreadID)) {
			return fmt.Errorf("discord scope requires concrete channel_id, guild_id and thread_id values when supplied")
		}
		return nil
	}
	return fmt.Errorf("scope does not match provider %q", provider)
}

// Provider returns the selected provider; Validate rejects empty/mixed scopes.
func (s Scope) Provider() string {
	switch {
	case s.Slack != nil:
		return ProviderSlack
	case s.GitHub != nil:
		return ProviderGitHub
	case s.Discord != nil:
		return ProviderDiscord
	default:
		return ""
	}
}

// Conversation is the canonical address within an app. Slack preserves
// existing thread (channel:timestamp) and DM addresses. GitHub PR identity uses
// the immutable repository ID; inline threads are tool arguments within that same PR.
func (s Scope) Conversation() (kind, key string, err error) {
	if err := s.Validate(s.Provider()); err != nil {
		return "", "", err
	}
	switch {
	case s.Slack != nil:
		if s.Slack.ThreadTS != "" {
			return "thread", s.Slack.ChannelID + ":" + s.Slack.ThreadTS, nil
		}
		if strings.HasPrefix(s.Slack.ChannelID, "D") {
			return "dm", s.Slack.ChannelID, nil
		}
		return "channel", s.Slack.ChannelID, nil
	case s.GitHub != nil:
		return "pull_request", strconv.FormatInt(
			s.GitHub.RepositoryID,
			10,
		) + "#" + strconv.Itoa(
			s.GitHub.PullRequest,
		), nil
	case s.Discord != nil:
		if s.Discord.ThreadID != "" {
			return "thread", s.Discord.ChannelID + ":" + s.Discord.ThreadID, nil
		}
		return "channel", s.Discord.ChannelID, nil
	default:
		return "", "", fmt.Errorf("scope must contain a supported provider address")
	}
}
