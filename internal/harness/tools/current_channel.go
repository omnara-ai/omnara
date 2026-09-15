package tools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func parseSetCurrentChannelRequest(raw json.RawMessage) (storage.ID, error) {
	var request struct {
		ChannelID json.RawMessage `json:"channel_id"`
	}
	if err := decodeSingleStrictJSON(raw, &request, "set_current_channel request"); err != nil {
		return storage.NilID, err
	}
	if len(request.ChannelID) == 0 {
		return storage.NilID, errors.New("channel_id is required; use null to clear the current channel")
	}
	var channelID *string
	if err := json.Unmarshal(request.ChannelID, &channelID); err != nil {
		return storage.NilID, errors.New("channel_id must be a channel ID or null")
	}
	if channelID == nil {
		return storage.NilID, nil
	}
	return publicid.Decode(publicid.KindIntegrationTarget, *channelID)
}

func currentChannelPublicID(id storage.ID) (*string, error) {
	if id == storage.NilID {
		return nil, nil //nolint:nilnil // No selection is represented as JSON null.
	}
	value, err := publicid.Encode(publicid.KindIntegrationTarget, id)
	return &value, err
}

func setCurrentChannel(ctx context.Context, call transactionalToolContext) (transactionalPhaseResult, error) {
	id, err := parseSetCurrentChannelRequest(call.Call.Input)
	if err != nil {
		return nil, err
	}
	channelID, err := currentChannelPublicID(id)
	if err != nil {
		return nil, err
	}
	content, err := structuredToolResultContent(struct {
		CurrentChannelID *string `json:"current_channel_id"`
	}{CurrentChannelID: channelID})
	if err != nil {
		return nil, err
	}
	completion, err := successfulToolCallCompletion(content)
	if err != nil {
		return nil, err
	}
	return executeInTransaction(executionstore.SetIntegrationTargetForToolCall(id, completion),
		func(err error) (transactionalPhaseResult, error) {
			if !errors.Is(err, storeerr.ErrConflict) {
				return nil, err
			}
			return unavailableChannelResult()
		}), nil
}
