package appdefinition

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var slackWorkspace = regexp.MustCompile(`^T[A-Z0-9]+$`)

// CanonicalLauncherScope is shared by saved launcher setup and event-derived
// parent addresses. Conversation addresses use the same encoding as Scope.
func CanonicalLauncherScope(provider, kind, ref string) (string, string, error) {
	kind, ref = strings.TrimSpace(kind), strings.TrimSpace(ref)
	invalid := func() (string, string, error) {
		return "", "", fmt.Errorf("invalid %s launcher scope %q", provider, kind)
	}
	var scope Scope
	switch provider {
	case ProviderSlack:
		switch kind {
		case "workspace":
			if !slackWorkspace.MatchString(ref) {
				return invalid()
			}
			return kind, ref, nil
		case "channel", "dm":
			scope.Slack = &SlackScope{ChannelID: ref}
		case "thread":
			channel, timestamp, ok := strings.Cut(ref, ":")
			if !ok {
				return invalid()
			}
			scope.Slack = &SlackScope{ChannelID: channel, ThreadTS: timestamp}
		default:
			return invalid()
		}
	case ProviderGitHub:
		switch kind {
		case "installation":
			id, err := strconv.ParseInt(ref, 10, 64)
			if err != nil || id <= 0 {
				return invalid()
			}
			return kind, strconv.FormatInt(id, 10), nil
		case "repository", "pull_request":
			repository, number := ref, 1
			if kind == "pull_request" {
				var rawNumber string
				var ok bool
				repository, rawNumber, ok = strings.Cut(ref, "#")
				var err error
				number, err = strconv.Atoi(rawNumber)
				if !ok || err != nil || number <= 0 {
					return invalid()
				}
			}
			repositoryID, err := strconv.ParseInt(repository, 10, 64)
			if err != nil || repositoryID <= 0 {
				return invalid()
			}
			if kind == "repository" {
				return kind, strconv.FormatInt(repositoryID, 10), nil
			}
			scope.GitHub = &GitHubScope{RepositoryID: repositoryID, PullRequest: number}
		default:
			return invalid()
		}
	case ProviderDiscord:
		switch kind {
		case "guild":
			if !discordID.MatchString(ref) {
				return invalid()
			}
			return kind, ref, nil
		case "channel":
			scope.Discord = &DiscordScope{ChannelID: ref}
		case "thread":
			channel, thread, ok := strings.Cut(ref, ":")
			if !ok {
				return invalid()
			}
			scope.Discord = &DiscordScope{ChannelID: channel, ThreadID: thread}
		default:
			return invalid()
		}
	default:
		return invalid()
	}
	canonicalKind, canonicalRef, err := scope.Conversation()
	if err != nil || canonicalKind != kind {
		return invalid()
	}
	return canonicalKind, canonicalRef, nil
}
