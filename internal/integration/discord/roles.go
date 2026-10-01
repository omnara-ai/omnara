package discord

import (
	"context"
	"net/http"
	"slices"
)

func (c *Client) ResolveBotMention(ctx context.Context, event MessageEvent) (MessageEvent, error) {
	if event.MentionsBot || event.Self || event.Automated || event.Message.GuildID == "" ||
		len(event.Message.MentionRoles) == 0 {
		return event, nil
	}
	if !validID(event.Message.GuildID) {
		return MessageEvent{}, invalidResponse(false)
	}
	for _, id := range event.Message.MentionRoles {
		if !validID(id) {
			return MessageEvent{}, invalidResponse(false)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	var roles []struct {
		ID      string `json:"id"`
		Managed bool   `json:"managed"`
		Tags    struct {
			BotID string `json:"bot_id"`
		} `json:"tags"`
	}
	if err := c.json(ctx, http.MethodGet, "/guilds/"+event.Message.GuildID+"/roles", nil, &roles, false); err != nil {
		return MessageEvent{}, err
	}
	if roles == nil {
		return MessageEvent{}, invalidResponse(false)
	}
	for _, role := range roles {
		if !validID(role.ID) || (role.Tags.BotID != "" && !validID(role.Tags.BotID)) {
			return MessageEvent{}, invalidResponse(false)
		}
		if role.Managed && role.Tags.BotID == c.credentials.BotUserID && role.ID != event.Message.GuildID &&
			slices.Contains(event.Message.MentionRoles, role.ID) {
			event.MentionsBot = true
		}
	}
	return event, nil
}
