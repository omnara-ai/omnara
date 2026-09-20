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
	canonical, err := CanonicalDestinationConfig(provider, raw)
	if err != nil {
		return nil, err
	}
	var fields map[string]string
	if err := json.Unmarshal(canonical, &fields); err != nil {
		return nil, err
	}
	if fields["channel_id"] == "" {
		return nil, fmt.Errorf("scheduled destination requires channel_id")
	}
	if fields["thread_ts"] != "" || fields["thread_id"] != "" {
		return nil, fmt.Errorf("scheduled destination must be a parent channel, not a thread")
	}
	if provider == ProviderSlack && !strings.HasPrefix(fields["channel_id"], "C") &&
		!strings.HasPrefix(fields["channel_id"], "G") {
		return nil, fmt.Errorf("scheduled Slack destination must be a channel, not a direct message")
	}
	return canonical, nil
}
