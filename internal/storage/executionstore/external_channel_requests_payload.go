package executionstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func validateExternalChannelAcceptedPayload(
	ctx context.Context,
	q *dbsqlc.Queries,
	request ExternalChannelRequestRecord,
	access integrationstore.ChannelAccess,
) error {
	var destination channelconnector.OperationDestination
	decoder := json.NewDecoder(bytes.NewReader(request.Payload))
	decoder.DisallowUnknownFields()
	switch request.Operation {
	case channelconnector.OperationSend:
		var payload channelconnector.SendPayload
		if err := decoder.Decode(&payload); err != nil {
			return err
		}
		if err := payload.Message.Validate(); err != nil {
			return err
		}
		if (payload.Message.Text != "" && !access.Capabilities.Text) ||
			(len(payload.Message.ArtifactIDs) != 0 && !access.Capabilities.Artifacts) {
			return errors.New("channel does not support this message content")
		}
		if _, err := channelconnector.ValidateSendParams(access.SendParamsSchema, payload.Params); err != nil {
			return err
		}
		for _, raw := range payload.Message.ArtifactIDs {
			id, err := publicid.Decode(publicid.KindArtifact, raw)
			if err != nil {
				return err
			}
			if _, err := q.GetArtifact(ctx, dbsqlc.GetArtifactParams{
				ProjectID: request.ProjectID, AgentID: request.AgentID, ID: id,
			}); err != nil {
				return err
			}
		}
		destination = payload.Destination
	case channelconnector.OperationRead:
		var payload channelconnector.ReadPayload
		if err := decoder.Decode(&payload); err != nil {
			return err
		}
		if payload.Limit < 1 || payload.Limit > 100 || len(payload.Cursor) > 4096 {
			return errors.New("invalid bounded history payload")
		}
		destination = payload.Destination
	case channelconnector.OperationInteraction:
		var payload channelconnector.InteractionPayload
		if err := decoder.Decode(&payload); err != nil {
			return err
		}
		if err := payload.Validate(); err != nil {
			return err
		}
		interactionID, err := publicid.Decode(publicid.KindAgentInteraction, payload.InteractionID)
		if err != nil || interactionID != request.InteractionID {
			return errors.New("presentation interaction does not match its owner")
		}
		agentID, err := publicid.Decode(publicid.KindAgent, payload.AgentID)
		if err != nil || agentID != request.AgentID {
			return errors.New("presentation agent does not match its owner")
		}
		channelID, err := publicid.Decode(publicid.KindIntegrationTarget, payload.ChannelID)
		if err != nil || channelID != request.IntegrationTargetID {
			return errors.New("presentation channel does not match its pinned destination")
		}
		row, err := q.GetAgentInteraction(ctx, dbsqlc.GetAgentInteractionParams{
			ProjectID: request.ProjectID, AgentID: request.AgentID, ID: request.InteractionID,
		})
		if err != nil {
			return err
		}
		canonical, err := agentInteractionRecordFromSQLC(row).Form()
		if err != nil {
			return err
		}
		canonicalJSON, err := json.Marshal(canonical)
		if err != nil {
			return err
		}
		formJSON, err := json.Marshal(payload.Form)
		if err != nil {
			return err
		}
		if payload.Kind != row.InteractionKind || !sameJSON(canonicalJSON, formJSON) {
			return errors.New("presentation must contain the canonical interaction kind and form")
		}
		destination = payload.Destination
	default:
		return errors.New("invalid channel operation")
	}
	if destination.ImplementationKey != access.ImplementationKey || destination.ProviderRef != access.ProviderRef ||
		destination.ProviderRefKind != access.ProviderRefKind ||
		!sameJSON(normalizedJSON(destination.ProviderMetadata), normalizedJSON(access.ProviderMetadata)) {
		return errors.New("accepted channel destination no longer matches the authorized address")
	}
	return nil
}

func acceptedExternalReplyGrants(raw json.RawMessage) *channelconnector.ChannelGrants {
	var payload channelconnector.SendPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil
	}
	return payload.ReplyChannelGrants
}

func pinExternalReplyGrants(
	raw json.RawMessage,
	grants *integrationstore.ChannelGrants,
) (json.RawMessage, error) {
	var payload channelconnector.SendPayload
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return nil, err
	}
	var pinned *channelconnector.ChannelGrants
	if grants != nil {
		pinned = &channelconnector.ChannelGrants{
			Receive: grants.ReceiveAllowed, Read: grants.ReadAllowed, Send: grants.SendAllowed,
		}
	}
	if payload.ReplyChannelGrants != nil && (pinned == nil || *payload.ReplyChannelGrants != *pinned) {
		return nil, errors.New("caller reply grants do not match the selected binding")
	}
	payload.ReplyChannelGrants = pinned
	return json.Marshal(payload)
}
