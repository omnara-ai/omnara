package tools

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/publicid"
)

type channelSendRequest struct {
	ChannelID string
	Message   channelconnector.Message
	Params    json.RawMessage
}

// parseSendChannelMessageRequest preserves raw params and original tool input.
// In particular, only omission means defaults; encoding/json's null-to-zero
// conversions cannot make an explicitly null field valid.
func parseSendChannelMessageRequest(raw json.RawMessage) (channelSendRequest, error) {
	if err := modelenvelope.ValidateToolInput(raw); err != nil {
		return channelSendRequest{}, fmt.Errorf("parse send_channel_message request: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return channelSendRequest{}, err
	}
	var request channelSendRequest
	for name, value := range fields {
		switch name {
		case "channel_id":
			var id *string
			if err := json.Unmarshal(value, &id); err != nil || id == nil {
				return channelSendRequest{}, errors.New("channel_id must be an explicit channel ID")
			}
			if _, err := publicid.Decode(publicid.KindIntegrationTarget, *id); err != nil {
				return channelSendRequest{}, errors.New("channel_id must be an explicit channel ID")
			}
			request.ChannelID = *id
		case "message":
			message, err := parseChannelSendMessage(value)
			if err != nil {
				return channelSendRequest{}, err
			}
			request.Message = message
		case "params":
			if _, err := jsoncanonical.ParseObject(value, channelconnector.MaxMetadataBytes); err != nil {
				return channelSendRequest{}, fmt.Errorf("send params: %w", err)
			}
			request.Params = value
		default:
			return channelSendRequest{}, errors.New("send_channel_message request contains an unsupported field")
		}
	}
	if request.ChannelID == "" {
		return channelSendRequest{}, errors.New("channel_id is required")
	}
	if _, found := fields["message"]; !found {
		return channelSendRequest{}, errors.New("message is required")
	}
	return request, nil
}

func parseChannelSendMessage(raw json.RawMessage) (channelconnector.Message, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return channelconnector.Message{}, errors.New("message must be an object")
	}
	var message channelconnector.Message
	for name, value := range fields {
		switch name {
		case "text":
			var text *string
			if err := json.Unmarshal(value, &text); err != nil || text == nil || *text == "" {
				return channelconnector.Message{}, errors.New("message text must be a nonempty string when supplied")
			}
			message.Text = *text
		case "artifact_ids":
			if err := json.Unmarshal(value, &message.ArtifactIDs); err != nil || len(message.ArtifactIDs) == 0 {
				return channelconnector.Message{}, errors.New("message artifact_ids must be a nonempty array when supplied")
			}
		default:
			return channelconnector.Message{}, errors.New("message contains an unsupported field")
		}
	}
	if err := message.Validate(); err != nil {
		return channelconnector.Message{}, err
	}
	return message, nil
}
