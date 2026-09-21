package discord

import (
	"encoding/json"
	"errors"
)

type Dispatch struct {
	Type     string          `json:"type"`
	Sequence int64           `json:"sequence"`
	Data     json.RawMessage `json:"data"`
}

type MessageEvent struct {
	Message     Message `json:"message"`
	MentionsBot bool    `json:"mentions_bot"`
	Self        bool    `json:"self"`
	Automated   bool    `json:"automated"`
}

// NormalizeMessage does not subscribe anyone. Application intake uses these
// facts to select configured mentions and replies in durably followed threads.
func NormalizeMessage(dispatch Dispatch, botUserID string) (MessageEvent, bool, error) {
	if !validID(botUserID) {
		return MessageEvent{}, false, errors.New("invalid discord bot user ID")
	}
	if dispatch.Type != "MESSAGE_CREATE" {
		return MessageEvent{}, false, nil
	}
	var message Message
	if len(dispatch.Data) > ResponseMaxBytes || json.Unmarshal(dispatch.Data, &message) != nil ||
		!validID(message.ID) || !validID(message.ChannelID) || !validID(message.Author.ID) ||
		(message.GuildID != "" && !validID(message.GuildID)) {
		return MessageEvent{}, false, invalidResponse(false)
	}
	event := MessageEvent{Message: message, Self: message.Author.ID == botUserID,
		Automated: message.Author.Bot || message.WebhookID != ""}
	for _, user := range message.Mentions {
		if user.ID == botUserID {
			event.MentionsBot = true
		}
	}
	return event, true, nil
}
