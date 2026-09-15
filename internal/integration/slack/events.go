package slack

import (
	"bytes"
	"encoding/json"
	"strings"
)

const EventBodyMaxBytes = 1024 * 1024

type Identity struct {
	AppID       string
	WorkspaceID string
	BotUserID   string
}

type EventsEnvelope struct {
	Type           string          `json:"type"`
	Challenge      string          `json:"challenge"`
	TeamID         string          `json:"team_id"`
	APIAppID       string          `json:"api_app_id"`
	EventID        string          `json:"event_id"`
	Authorizations []Authorization `json:"authorizations"`
	Event          Event           `json:"-"`
	RawEvent       json.RawMessage `json:"event"`
}

type Authorization struct {
	TeamID string `json:"team_id"`
	UserID string `json:"user_id"`
	IsBot  bool   `json:"is_bot"`
}

type Event struct {
	Type        string        `json:"type"`
	Subtype     string        `json:"subtype"`
	User        string        `json:"user"`
	BotID       string        `json:"bot_id"`
	Text        string        `json:"text"`
	Channel     string        `json:"channel"`
	ChannelType string        `json:"channel_type"`
	TS          string        `json:"ts"`
	ThreadTS    string        `json:"thread_ts"`
	Team        string        `json:"team"`
	SourceTeam  string        `json:"source_team"`
	UserTeam    string        `json:"user_team"`
	Tokens      RevokedTokens `json:"tokens"`
}

type RevokedTokens struct {
	Bot []string `json:"bot"`
}

type UserProfile struct {
	DisplayName string `json:"display_name"`
	RealName    string `json:"real_name"`
	Name        string `json:"name"`
}

func DecodeEventsEnvelope(raw []byte) (EventsEnvelope, error) {
	var envelope EventsEnvelope
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&envelope); err != nil {
		return EventsEnvelope{}, err
	}
	if err := rejectTrailingJSON(decoder); err != nil {
		return EventsEnvelope{}, err
	}
	if len(envelope.RawEvent) == 0 {
		return envelope, nil
	}
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(envelope.RawEvent, &probe); err != nil {
		return EventsEnvelope{}, err
	}
	if nameUpdateEventTypes[probe.Type] {
		envelope.Event = Event{Type: probe.Type}
		return envelope, nil
	}
	if err := json.Unmarshal(envelope.RawEvent, &envelope.Event); err != nil {
		return EventsEnvelope{}, err
	}
	return envelope, nil
}

var nameUpdateEventTypes = map[string]bool{
	"user_profile_changed": true,
	"channel_rename":       true,
	"group_rename":         true,
}

type NameUpdate struct {
	UserID         string
	ConversationID string
	DisplayName    string
}

func EventNameUpdate(envelope EventsEnvelope) (NameUpdate, bool) {
	switch envelope.Event.Type {
	case "user_profile_changed":
		var payload struct {
			User struct {
				ID       string       `json:"id"`
				Name     string       `json:"name"`
				RealName string       `json:"real_name"`
				Profile  *UserProfile `json:"profile"`
			} `json:"user"`
		}
		if err := json.Unmarshal(envelope.RawEvent, &payload); err != nil {
			return NameUpdate{}, false
		}
		name := userInfoDisplayName(slackUserInfo{
			Name:     payload.User.Name,
			RealName: payload.User.RealName,
			Profile:  payload.User.Profile,
		})
		if payload.User.ID == "" || name == "" {
			return NameUpdate{}, false
		}
		return NameUpdate{UserID: payload.User.ID, DisplayName: name}, true
	case "channel_rename", "group_rename":
		var payload struct {
			Channel struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"channel"`
		}
		if err := json.Unmarshal(envelope.RawEvent, &payload); err != nil {
			return NameUpdate{}, false
		}
		conversationID := strings.TrimSpace(payload.Channel.ID)
		name := strings.TrimSpace(payload.Channel.Name)
		if conversationID == "" || name == "" {
			return NameUpdate{}, false
		}
		return NameUpdate{ConversationID: conversationID, DisplayName: name}, true
	default:
		return NameUpdate{}, false
	}
}

func URLVerificationChallenge(envelope EventsEnvelope) (string, bool) {
	return envelope.Challenge, envelope.Type == "url_verification"
}

func EventCallbackEnvelope(envelope EventsEnvelope) bool {
	return envelope.Type == "event_callback"
}

func ValidateRuntimeBotAuthorization(identity Identity, envelope EventsEnvelope) bool {
	if !ValidateEnvelopeIdentity(identity, envelope) {
		return false
	}
	if len(envelope.Authorizations) == 0 {
		return false
	}
	for _, authorization := range envelope.Authorizations {
		if authorization.TeamID == identity.WorkspaceID && authorization.UserID == identity.BotUserID &&
			authorization.IsBot {
			return true
		}
	}
	return false
}

func ValidateEnvelopeIdentity(identity Identity, envelope EventsEnvelope) bool {
	return envelope.APIAppID == identity.AppID && envelope.TeamID == identity.WorkspaceID
}

func DisabledInstallEvent(botUserID string, event Event) bool {
	return event.Type == "app_uninstalled" || revokedInstallToken(botUserID, event)
}

func IgnoredLifecycleEvent(botUserID string, event Event) bool {
	return event.Type == "tokens_revoked" && !revokedInstallToken(botUserID, event)
}

func revokedInstallToken(botUserID string, event Event) bool {
	if event.Type != "tokens_revoked" {
		return false
	}
	for _, revokedBotUserID := range event.Tokens.Bot {
		if revokedBotUserID == botUserID {
			return true
		}
	}
	return false
}
