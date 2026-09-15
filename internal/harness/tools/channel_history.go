package tools

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/publicid"
)

const (
	defaultChannelHistoryLimit           = 50
	maxChannelHistoryLimit               = 100
	maxChannelHistoryProviderCursorBytes = 4 * 1024
	maxChannelHistoryCursorBytes         = 8 * 1024
)

// channelHistoryRequest is parsed execution input, not a replacement for the
// original tool arguments. ProviderCursor is decoded only after checking scope.
type channelHistoryRequest struct {
	ChannelID      uuid.UUID
	Limit          int
	ProviderCursor string
}

func resolveChannelHistoryRequest(raw json.RawMessage, turn Turn) (channelHistoryRequest, error) {
	fields, err := jsoncanonical.ParseObject(raw, modelenvelope.DefaultMaxProviderResponseBytes)
	if err != nil {
		return channelHistoryRequest{}, errors.New("read_channel requires one unambiguous JSON object")
	}
	request := channelHistoryRequest{Limit: defaultChannelHistoryLimit}
	var cursor string
	for name, value := range fields {
		switch name {
		case "channel_id":
			text, ok := value.(string)
			if !ok {
				return channelHistoryRequest{}, errors.New("read_channel channel_id must be a channel ID")
			}
			request.ChannelID, err = publicid.Decode(publicid.KindIntegrationTarget, text)
			if err != nil {
				return channelHistoryRequest{}, errors.New("read_channel channel_id must be a channel ID")
			}
		case "limit":
			number, ok := value.(json.Number)
			if !ok {
				return channelHistoryRequest{}, errors.New("read_channel limit must be an integer between 1 and 100")
			}
			limit, err := number.Int64()
			if err != nil || limit < 1 || limit > maxChannelHistoryLimit {
				return channelHistoryRequest{}, errors.New("read_channel limit must be an integer between 1 and 100")
			}
			request.Limit = int(limit)
		case "cursor":
			var ok bool
			cursor, ok = value.(string)
			if !ok || cursor == "" {
				return channelHistoryRequest{}, errors.New("read_channel cursor must be a nonempty string")
			}
		default:
			return channelHistoryRequest{}, errors.New("read_channel request contains an unsupported field")
		}
	}
	if request.ChannelID == uuid.Nil {
		return channelHistoryRequest{}, errors.New("read_channel channel_id is required")
	}
	if cursor != "" {
		request.ProviderCursor, err = decodeChannelHistoryCursor(cursor, turn, request.ChannelID)
		if err != nil {
			return channelHistoryRequest{}, err
		}
	}
	return request, nil
}

type channelHistoryCursor struct {
	ProjectID string `json:"project_id"`
	AgentID   string `json:"agent_id"`
	ChannelID string `json:"channel_id"`
	// Encoding the provider bytes bounds JSON escaping overhead for every valid
	// UTF-8 cursor, including quotes/control characters, without altering it.
	ProviderCursor string `json:"provider_cursor"`
}

func decodeChannelHistoryCursor(raw string, turn Turn, channelID uuid.UUID) (string, error) {
	invalid := errors.New("invalid read_channel cursor")
	if raw == "" || len(raw) > maxChannelHistoryCursorBytes {
		return "", invalid
	}
	payload, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil || base64.RawURLEncoding.EncodeToString(payload) != raw {
		return "", invalid
	}
	fields, err := jsoncanonical.ParseObject(payload, maxChannelHistoryCursorBytes)
	if err != nil || len(fields) != 4 {
		return "", invalid
	}
	var cursor channelHistoryCursor
	for name, value := range fields {
		text, ok := value.(string)
		if !ok {
			return "", invalid
		}
		switch name {
		case "project_id":
			cursor.ProjectID = text
		case "agent_id":
			cursor.AgentID = text
		case "channel_id":
			cursor.ChannelID = text
		case "provider_cursor":
			cursor.ProviderCursor = text
		default:
			return "", invalid
		}
	}
	project, projectErr := publicid.Decode(publicid.KindProject, cursor.ProjectID)
	agent, agentErr := publicid.Decode(publicid.KindAgent, cursor.AgentID)
	channel, channelErr := publicid.Decode(publicid.KindIntegrationTarget, cursor.ChannelID)
	if projectErr != nil || agentErr != nil || channelErr != nil {
		return "", invalid
	}
	if project != turn.ProjectID || agent != turn.AgentID || channel != channelID {
		return "", errors.New("read_channel cursor belongs to a different project, agent, or channel")
	}
	provider, err := base64.RawURLEncoding.Strict().DecodeString(cursor.ProviderCursor)
	if err != nil || len(provider) == 0 || len(provider) > maxChannelHistoryProviderCursorBytes ||
		!utf8.Valid(provider) || base64.RawURLEncoding.EncodeToString(provider) != cursor.ProviderCursor {
		return "", invalid
	}
	return string(provider), nil
}
