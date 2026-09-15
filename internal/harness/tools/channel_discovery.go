package tools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type getChannelRequest struct {
	ChannelID string `json:"channel_id"`
}

type channelDescription struct {
	IsCurrent        bool                                 `json:"is_current"`
	ChannelID        string                               `json:"channel_id"`
	ParentChannelID  string                               `json:"parent_channel_id,omitempty"`
	Kind             integrationstore.ChannelKind         `json:"kind"`
	Name             string                               `json:"name"`
	Description      string                               `json:"description"`
	Active           bool                                 `json:"active"`
	CanReceive       bool                                 `json:"can_receive"`
	Capabilities     integrationstore.ChannelCapabilities `json:"capabilities"`
	SendParamsSchema json.RawMessage                      `json:"send_params_schema"`
}

func parseGetChannelRequest(raw json.RawMessage) (integrationstore.ID, error) {
	var input getChannelRequest
	if err := decodeSingleStrictJSON(raw, &input, "get_channel request"); err != nil {
		return integrationstore.NilID, err
	}
	return publicid.Decode(publicid.KindIntegrationTarget, input.ChannelID)
}

func getChannel(ctx context.Context, call transactionalToolContext) (transactionalPhaseResult, error) {
	id, err := parseGetChannelRequest(call.Call.Input)
	if err != nil {
		return nil, err
	}
	access, err := call.Reader.GetChannelAccess(ctx, id)
	if errors.Is(err, storeerr.ErrNotFound) {
		return unavailableChannelResult()
	}
	if err != nil {
		return nil, err
	}
	description, err := describeChannel(access)
	if err != nil {
		return nil, err
	}
	currentID, err := call.Reader.CurrentChannelID(ctx)
	if err != nil {
		return nil, err
	}
	description.IsCurrent = currentID == id
	content, err := structuredToolResultContent(description)
	if err != nil {
		return nil, err
	}
	return completeInTransaction(content), nil
}

func describeChannel(access integrationstore.ChannelAccess) (channelDescription, error) {
	id, err := publicid.Encode(publicid.KindIntegrationTarget, access.ChannelID)
	if err != nil {
		return channelDescription{}, err
	}
	parent := ""
	if access.ParentChannelID != integrationstore.NilID {
		parent, err = publicid.Encode(publicid.KindIntegrationTarget, access.ParentChannelID)
		if err != nil {
			return channelDescription{}, err
		}
	}
	return channelDescription{
		ChannelID: id, ParentChannelID: parent, Kind: access.Kind,
		Name: access.Name, Description: access.Description, Active: access.Active,
		CanReceive: access.ReceiveAllowed, Capabilities: access.Capabilities,
		SendParamsSchema: access.SendParamsSchema,
	}, nil
}

func unavailableChannelResult() (transactionalPhaseResult, error) {
	cause := errors.New("channel is unavailable or is not attached to this agent")
	content, err := structuredToolResultContent(map[string]string{
		"error": cause.Error(), "error_code": "unavailable_channel",
	})
	if err != nil {
		return nil, err
	}
	return failInTransaction(content, cause), nil
}
