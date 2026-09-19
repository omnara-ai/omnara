// Package appdefinition describes config attachments without depending on
// storage, provider transports, or the agent compiler. Launchers and credentials
// belong to project setup; none are part of these immutable agent capabilities.
package appdefinition

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	ProviderSlack       = "slack"
	ProviderGitHub      = "github"
	ProviderDiscord     = "discord"
	Slack               = "omnara.slack"
	GitHub              = "omnara.github"
	Discord             = "omnara.discord"
	SlackInteractions   = "omnara.slack.interactions"
	DiscordInteractions = "omnara.discord.interactions"
)

type Definition struct {
	ID                 string
	Provider           string
	ListenerEvents     []string
	InteractionHandler string
}

// Lookup returns built-in app metadata reviewed and shipped with the server.
// Project setup configures these definitions; it cannot register new ones.
func Lookup(id string) (Definition, bool) {
	switch id {
	case Slack:
		return Definition{id, ProviderSlack, []string{"message"}, SlackInteractions}, true
	case GitHub:
		return Definition{id, ProviderGitHub, []string{"discussion_comment", "review_comment", "commit"}, ""}, true
	case Discord:
		return Definition{id, ProviderDiscord, []string{"message"}, DiscordInteractions}, true
	default:
		return Definition{}, false
	}
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

type Listener struct {
	Events []string `json:"events"`
}

// Follow permits registering the resulting conversation after a confirmed post.
// It does not itself subscribe to a channel or imply a tool or interaction handler.
type Follow struct {
	Replies bool `json:"replies"`
}

type InteractionHandler struct {
	Definition string `json:"definition"`
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

// ValidateCapabilities checks only the selected capabilities. In particular a
// handler needs no listener, and a follow policy needs no broad subscription.
func (d Definition) ValidateCapabilities(
	scope *Scope,
	listener *Listener,
	follow *Follow,
	handler *InteractionHandler,
) error {
	if scope != nil {
		if err := scope.Validate(d.Provider); err != nil {
			return err
		}
	}
	if (listener != nil || follow != nil || handler != nil) && scope == nil {
		return fmt.Errorf("selected listener, follow or interaction handler requires a scope")
	}
	return d.ValidateSelection(listener, handler)
}

// ValidateSelection checks exported receive/presentation capabilities without a
// concrete scope. Project app templates may obtain their scope from a launcher.
func (d Definition) ValidateSelection(listener *Listener, handler *InteractionHandler) error {
	if listener != nil {
		if len(listener.Events) == 0 {
			return fmt.Errorf("listener requires at least one event")
		}
		seen := map[string]bool{}
		for _, event := range listener.Events {
			if seen[event] || !slices.Contains(d.ListenerEvents, event) {
				return fmt.Errorf("invalid or duplicate listener event %q for %s", event, d.ID)
			}
			seen[event] = true
		}
	}
	if handler != nil && (d.InteractionHandler == "" || handler.Definition != d.InteractionHandler) {
		return fmt.Errorf("interaction handler %q is not supported by %s", handler.Definition, d.ID)
	}
	return nil
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

// Conversation is the canonical address within a connection. Slack preserves
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

// Contains checks only provider scope. Callers must also check connection
// identity and live authorization. GitHub PRs match exactly;
// a channel scope may contain its own threads, never a different channel.
func (s Scope) Contains(child Scope) bool {
	provider := s.Provider()
	if s.Validate(provider) != nil || child.Validate(provider) != nil {
		return false
	}
	switch provider {
	case ProviderSlack:
		return s.Slack.ChannelID == child.Slack.ChannelID &&
			(s.Slack.ThreadTS == "" || s.Slack.ThreadTS == child.Slack.ThreadTS)
	case ProviderDiscord:
		return s.Discord.ChannelID == child.Discord.ChannelID &&
			(s.Discord.GuildID == "" || s.Discord.GuildID == child.Discord.GuildID) &&
			(s.Discord.ThreadID == "" || s.Discord.ThreadID == child.Discord.ThreadID)
	case ProviderGitHub:
		return *s.GitHub == *child.GitHub
	default:
		return false
	}
}
