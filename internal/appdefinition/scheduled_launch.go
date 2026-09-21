package appdefinition

import (
	"encoding/json"
	"fmt"
	"strings"
)

// CanonicalScheduledDestination accepts only a parent channel for a new thread.
// Provider adapters verify Discord's channel type before publishing.
func CanonicalScheduledDestination(provider string, raw json.RawMessage) (json.RawMessage, error) {
	if provider != ProviderSlack && provider != ProviderDiscord {
		return nil, fmt.Errorf("scheduled launches require a Slack or Discord app")
	}
	scope, err := ResolveDestination(provider, raw)
	if err != nil {
		return nil, err
	}
	if (scope.Slack != nil && scope.Slack.ThreadTS != "") ||
		(scope.Discord != nil && scope.Discord.ThreadID != "") {
		return nil, fmt.Errorf("scheduled destination must be a parent channel, not a thread")
	}
	if scope.Slack != nil && !strings.HasPrefix(scope.Slack.ChannelID, "C") &&
		!strings.HasPrefix(scope.Slack.ChannelID, "G") {
		return nil, fmt.Errorf("scheduled Slack destination must be a channel, not a direct message")
	}
	return scope.ConversationJSON()
}
