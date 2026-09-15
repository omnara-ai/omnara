package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

type channelListResult struct {
	CurrentChannelID *string           `json:"current_channel_id"`
	Channels         []channelListItem `json:"channels"`
	NextCursor       string            `json:"next_cursor,omitempty"`
}

type channelListRequest struct {
	Cursor          string `json:"cursor,omitempty"`
	Limit           int    `json:"limit,omitempty"`
	ParentChannelID string `json:"parent_channel_id,omitempty"`

	// Keep parsed state out of the canonical permission request.
	decodedCursor *channelListCursor
}

type channelListCursor struct {
	CreatedAt       time.Time `json:"created_at"`
	ChannelID       string    `json:"channel_id"`
	ProjectID       string    `json:"project_id"`
	AgentID         string    `json:"agent_id"`
	ParentChannelID string    `json:"parent_channel_id,omitempty"`

	targetID uuid.UUID
}

type channelListItem struct {
	ChannelID       string `json:"channel_id"`
	ParentChannelID string `json:"parent_channel_id,omitempty"`
	Provider        string `json:"provider"`
	AddressKind     string `json:"address_kind"`
	Name            string `json:"name"`
	State           string `json:"state"`
	CanReceive      bool   `json:"can_receive"`
	CanRead         bool   `json:"can_read"`
	CanSend         bool   `json:"can_send"`
}

func listChannels(
	ctx context.Context,
	call transactionalToolContext,
) (transactionalPhaseResult, error) {
	request, pageInput, err := resolveChannelListRequest(call.Call.Input)
	if err != nil {
		return nil, err
	}
	if request.decodedCursor != nil && !request.decodedCursor.matches(call.Turn, request.ParentChannelID) {
		return nil, errors.New("list_channels cursor belongs to a different agent or parent filter")
	}
	authorizationInput, err := marshalJSON(request)
	if err != nil {
		return nil, err
	}
	if err := authorizeToolExecution(
		ctx,
		call.Reader,
		call.Turn,
		call.Call,
		authorizationInput,
	); err != nil {
		return nil, err
	}
	page, err := call.Reader.ListAgentChannelTargets(ctx, pageInput)
	if err != nil {
		return nil, err
	}
	items := make([]channelListItem, 0, len(page.Targets))
	for _, target := range page.Targets {
		channelID, err := publicid.Encode(publicid.KindIntegrationTarget, target.ID)
		if err != nil {
			return nil, err
		}
		active := target.InstallState == integrationstore.IntegrationInstallStateActive &&
			target.AppState == integrationstore.IntegrationAppStateActive
		state := "disabled"
		if active {
			state = "active"
		}
		name := strings.TrimSpace(target.DisplayName)
		if name == "" {
			name = target.Provider + " " + target.ProviderRefKind
		}
		parentID := ""
		if target.ParentChannelID != uuid.Nil {
			parentID, err = publicid.Encode(publicid.KindIntegrationTarget, target.ParentChannelID)
			if err != nil {
				return nil, err
			}
		}
		items = append(items, channelListItem{
			ChannelID: channelID, ParentChannelID: parentID, Provider: target.Provider,
			AddressKind: target.ProviderRefKind, Name: name, State: state,
			CanReceive: active && target.ReceiveAllowed,
			CanRead:    active && target.ReadAllowed,
			CanSend:    active && target.SendAllowed,
		})
	}
	result := channelListResult{Channels: items}
	currentID, err := call.Reader.CurrentChannelID(ctx)
	if err != nil {
		return nil, err
	}
	result.CurrentChannelID, err = currentChannelPublicID(currentID)
	if err != nil {
		return nil, err
	}
	if page.Next != nil {
		result.NextCursor, err = encodeChannelListCursor(*page.Next, call.Turn, request.ParentChannelID)
		if err != nil {
			return nil, err
		}
	}
	content, err := structuredToolResultContent(result)
	if err != nil {
		return nil, err
	}
	return completeInTransaction(content), nil
}

func resolveChannelListRequest(
	raw json.RawMessage,
) (channelListRequest, integrationstore.ListAgentChannelTargetsInput, error) {
	request := channelListRequest{}
	if len(raw) != 0 {
		if err := decodeSingleStrictJSON(raw, &request, "list_channels request"); err != nil {
			return channelListRequest{}, integrationstore.ListAgentChannelTargetsInput{}, err
		}
	}
	if len(request.Cursor) > 1024 {
		return channelListRequest{}, integrationstore.ListAgentChannelTargetsInput{}, errors.New(
			"list_channels cursor exceeds its size limit",
		)
	}
	if request.Limit == 0 {
		request.Limit = 50
	}
	if request.Limit < 1 || request.Limit > integrationstore.MaxAgentChannelTargetsPageSize {
		return channelListRequest{}, integrationstore.ListAgentChannelTargetsInput{}, fmt.Errorf(
			"list_channels limit must be between 1 and %d",
			integrationstore.MaxAgentChannelTargetsPageSize,
		)
	}
	input := integrationstore.ListAgentChannelTargetsInput{Limit: request.Limit}
	if request.ParentChannelID != "" {
		id, err := publicid.Decode(publicid.KindIntegrationTarget, request.ParentChannelID)
		if err != nil {
			return channelListRequest{}, integrationstore.ListAgentChannelTargetsInput{}, errors.New("invalid parent_channel_id")
		}
		input.ParentChannelID = id
	}
	if request.Cursor != "" {
		cursor, err := decodeChannelListCursor(request.Cursor)
		if err != nil {
			return channelListRequest{}, integrationstore.ListAgentChannelTargetsInput{}, err
		}
		request.decodedCursor = &cursor
		input.After = &integrationstore.AgentChannelTargetCursor{CreatedAt: cursor.CreatedAt, ID: cursor.targetID}
	}
	return request, input, nil
}

func encodeChannelListCursor(cursor integrationstore.AgentChannelTargetCursor,
	turn Turn,
	parentID string) (string,
	error) {
	channelID, err := publicid.Encode(publicid.KindIntegrationTarget, cursor.ID)
	if err != nil {
		return "", err
	}
	projectID, err := publicid.Encode(publicid.KindProject, turn.ProjectID)
	if err != nil {
		return "", err
	}
	agentID, err := publicid.Encode(publicid.KindAgent, turn.AgentID)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(channelListCursor{
		CreatedAt: cursor.CreatedAt.UTC(),
		ChannelID: channelID,
		ProjectID: projectID, AgentID: agentID, ParentChannelID: parentID,
	})
	if err != nil {
		return "", fmt.Errorf("encode channel cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeChannelListCursor(raw string) (channelListCursor, error) {
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(payload) > 512 {
		return channelListCursor{}, errors.New("invalid list_channels cursor")
	}
	var cursor channelListCursor
	if err := decodeSingleStrictJSON(payload, &cursor, "list_channels cursor"); err != nil {
		return channelListCursor{}, errors.New("invalid list_channels cursor")
	}
	cursor.targetID, err = publicid.Decode(publicid.KindIntegrationTarget, cursor.ChannelID)
	if err != nil || cursor.CreatedAt.IsZero() {
		return channelListCursor{}, errors.New("invalid list_channels cursor")
	}
	return cursor, nil
}

func (cursor channelListCursor) matches(turn Turn, parentID string) bool {
	projectID, projectErr := publicid.Decode(publicid.KindProject, cursor.ProjectID)
	agentID, agentErr := publicid.Decode(publicid.KindAgent, cursor.AgentID)
	return projectErr == nil && agentErr == nil && projectID == turn.ProjectID &&
		agentID == turn.AgentID && cursor.ParentChannelID == parentID
}
