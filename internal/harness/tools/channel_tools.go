package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const channelDeliveryWait = 20 * time.Second

var (
	errMissingChannel              = errors.New("missing_channel")
	errAmbiguousChannel            = errors.New("ambiguous_channel")
	errChannelDisabled             = errors.New("channel_disabled")
	errUnsupportedChannelTransport = errors.New("unsupported_channel_transport")
)

type channelDestination struct {
	Binding integrationstore.IntegrationTargetBindingRecord
	Target  integrationstore.IntegrationTargetRecord
	Install integrationstore.IntegrationInstallRecord
	App     integrationstore.IntegrationAppRecord
}

type channelListResult struct {
	Channels   []channelListItem `json:"channels"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

type channelListRequest struct {
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

type channelListCursor struct {
	CreatedAt time.Time `json:"created_at"`
	ChannelID string    `json:"channel_id"`
}

type channelListItem struct {
	ChannelID   string `json:"channel_id"`
	Provider    string `json:"provider"`
	AddressKind string `json:"address_kind"`
	Name        string `json:"name"`
	State       string `json:"state"`
	CanReceive  bool   `json:"can_receive"`
	CanSend     bool   `json:"can_send"`
}

type channelOutboundPayloadV1 struct {
	Destination channelOutboundDestinationV1 `json:"destination"`
	Message     channelOutboundMessageV1     `json:"message"`
	Context     channelOutboundContextV1     `json:"context"`
}

type channelOutboundDestinationV1 struct {
	ChannelID        string          `json:"channel_id"`
	ProviderRefKind  string          `json:"provider_ref_kind"`
	ProviderRef      string          `json:"provider_ref"`
	ProviderMetadata json.RawMessage `json:"provider_metadata"`
}

type channelOutboundMessageV1 struct {
	Text string `json:"text"`
}

type channelOutboundContextV1 struct {
	AgentID        string `json:"agent_id"`
	ProviderCallID string `json:"provider_call_id"`
}

type connectorChannelDeliveryIntent struct {
	Kind             string
	PayloadVersion   string
	Payload          json.RawMessage
	IdempotencyScope string
	IdempotencyKey   string
	NotifyRef        storage.ID
}

func listChannels(
	ctx context.Context,
	call transactionalToolContext,
) (transactionalPhaseResult, error) {
	request, pageInput, err := resolveChannelListRequest(call.Call.Input)
	if err != nil {
		return nil, err
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
		items = append(items, channelListItem{
			ChannelID: channelID, Provider: target.Provider,
			AddressKind: target.ProviderRefKind, Name: name, State: state,
			CanReceive: active && target.ReceiveAllowed,
			CanSend:    active && target.SendAllowed,
		})
	}
	result := channelListResult{Channels: items}
	if page.Next != nil {
		result.NextCursor, err = encodeChannelListCursor(*page.Next)
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
	if request.Cursor != "" {
		cursor, err := decodeChannelListCursor(request.Cursor)
		if err != nil {
			return channelListRequest{}, integrationstore.ListAgentChannelTargetsInput{}, err
		}
		input.After = &cursor
	}
	return request, input, nil
}

func encodeChannelListCursor(cursor integrationstore.AgentChannelTargetCursor) (string, error) {
	channelID, err := publicid.Encode(publicid.KindIntegrationTarget, cursor.ID)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(channelListCursor{
		CreatedAt: cursor.CreatedAt.UTC(),
		ChannelID: channelID,
	})
	if err != nil {
		return "", fmt.Errorf("encode channel cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeChannelListCursor(raw string) (integrationstore.AgentChannelTargetCursor, error) {
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(payload) > 512 {
		return integrationstore.AgentChannelTargetCursor{}, errors.New("invalid list_channels cursor")
	}
	var cursor channelListCursor
	if err := decodeSingleStrictJSON(payload, &cursor, "list_channels cursor"); err != nil {
		return integrationstore.AgentChannelTargetCursor{}, errors.New("invalid list_channels cursor")
	}
	id, err := publicid.Decode(publicid.KindIntegrationTarget, cursor.ChannelID)
	if err != nil || cursor.CreatedAt.IsZero() {
		return integrationstore.AgentChannelTargetCursor{}, errors.New("invalid list_channels cursor")
	}
	return integrationstore.AgentChannelTargetCursor{CreatedAt: cursor.CreatedAt, ID: id}, nil
}

func runChannelMessageAsync(
	ctx context.Context,
	call asyncToolContext,
) (asyncPhaseResult, error) {
	record, err := call.Executor.Store.Execution().GetToolCall(
		ctx,
		call.Turn.ProjectID,
		call.Turn.AgentID,
		call.ToolCallID,
	)
	if err != nil {
		return nil, err
	}
	input, err := resolveChannelMessageRequest(call.Call.Input)
	if err != nil {
		return nil, err
	}
	destination, err := call.Executor.resolveChannelDestination(ctx, call.Turn, input.ChannelID)
	if err != nil {
		content, resultErr := channelDestinationFailureResult(input.ChannelID, err)
		if resultErr != nil {
			return nil, resultErr
		}
		return failAsynchronously(content, err), nil
	}

	if destination.App.ConnectorKey == "native_slack_v1" &&
		destination.Install.Provider == integrationstore.IntegrationProviderSlack {
		target, err := call.Executor.nativeSlackChannelTarget(ctx, call.Turn, destination)
		if err != nil {
			content, resultErr := channelDestinationFailureResult(input.ChannelID, err)
			if resultErr != nil {
				return nil, resultErr
			}
			return failAsynchronously(content, err), nil
		}
		content, sendErr := call.Executor.dispatchIntegrationMessageToTarget(
			ctx,
			call.Turn,
			target,
			record,
			input.Text,
		)
		if sendErr != nil {
			return failAsynchronously(content, sendErr), nil
		}
		return completeAsynchronously(content), nil
	}
	if strings.HasPrefix(destination.App.ConnectorKey, "native_") {
		content, resultErr := channelDestinationFailureResult(
			input.ChannelID,
			errUnsupportedChannelTransport,
		)
		if resultErr != nil {
			return nil, resultErr
		}
		return failAsynchronously(content, errUnsupportedChannelTransport), nil
	}

	delivery, err := call.Executor.enqueueChannelDelivery(
		ctx,
		call.Turn,
		record,
		destination,
		input.Text,
	)
	if err != nil {
		content, resultErr := channelDestinationFailureResult(input.ChannelID, err)
		if resultErr != nil {
			return nil, resultErr
		}
		return failAsynchronously(content, err), nil
	}
	delivery, err = call.Executor.waitForChannelDelivery(ctx, delivery)
	if err != nil {
		return nil, err
	}
	return channelDeliveryToolResult(delivery)
}

func (e Executor) resolveChannelDestination(
	ctx context.Context,
	turn Turn,
	channelID string,
) (channelDestination, error) {
	var binding integrationstore.IntegrationTargetBindingRecord
	if channelID != "" {
		targetID, err := publicid.Decode(publicid.KindIntegrationTarget, channelID)
		if err != nil {
			return channelDestination{}, errMissingChannel
		}
		binding, err = e.Store.Integrations().GetActiveSendBindingForTarget(
			ctx,
			turn.ProjectID,
			turn.AgentID,
			targetID,
		)
		if errors.Is(err, storeerr.ErrNotFound) {
			return channelDestination{}, errMissingChannel
		}
		if err != nil {
			return channelDestination{}, err
		}
	} else {
		originTargets, err := e.channelOriginTargets(ctx, turn)
		if err != nil {
			return channelDestination{}, err
		}
		if len(originTargets) > 1 {
			return channelDestination{}, errAmbiguousChannel
		}
		if len(originTargets) == 1 {
			origin, found, originErr := e.latestChannelOrigin(ctx, turn)
			if originErr != nil {
				return channelDestination{}, originErr
			}
			if !found || origin.TargetID != originTargets[0] {
				return channelDestination{}, errChannelDisabled
			}
			binding, err = e.activeSendBindingForOrigin(ctx, turn, origin)
			if errors.Is(err, storeerr.ErrNotFound) {
				return channelDestination{}, errChannelDisabled
			}
			if err != nil {
				return channelDestination{}, err
			}
		} else {
			return channelDestination{}, errMissingChannel
		}
	}

	return e.resolveChannelDestinationFromBinding(ctx, turn, binding)
}

func (e Executor) resolveChannelDestinationFromBinding(
	ctx context.Context,
	turn Turn,
	binding integrationstore.IntegrationTargetBindingRecord,
) (channelDestination, error) {
	target, err := e.Store.Integrations().GetIntegrationTarget(
		ctx,
		turn.ProjectID,
		binding.IntegrationTargetID,
	)
	if err != nil {
		return channelDestination{}, err
	}
	if target.IntegrationInstallID != binding.IntegrationInstallID {
		return channelDestination{}, errMissingChannel
	}
	install, err := e.Store.Integrations().GetIntegrationInstall(
		ctx,
		turn.ProjectID,
		binding.IntegrationInstallID,
	)
	if err != nil {
		return channelDestination{}, err
	}
	if install.State != integrationstore.IntegrationInstallStateActive {
		return channelDestination{}, errChannelDisabled
	}
	app, err := e.Store.Integrations().GetIntegrationApp(ctx, install.OrgID, install.IntegrationAppID)
	if err != nil {
		return channelDestination{}, err
	}
	if app.State != integrationstore.IntegrationAppStateActive {
		return channelDestination{}, errChannelDisabled
	}
	return channelDestination{Binding: binding, Target: target, Install: install, App: app}, nil
}

func (e Executor) resolveAutomaticChannelDestination(
	ctx context.Context,
	turn Turn,
) (channelDestination, error) {
	origin, found, err := e.latestChannelOrigin(ctx, turn)
	if err != nil {
		return channelDestination{}, err
	}
	if found {
		binding, err := e.activeSendBindingForOrigin(ctx, turn, origin)
		if errors.Is(err, storeerr.ErrNotFound) {
			return channelDestination{}, errChannelDisabled
		}
		if err != nil {
			return channelDestination{}, err
		}
		return e.resolveChannelDestinationFromBinding(ctx, turn, binding)
	}

	// Native Slack prompts and notices historically follow the agent's sticky
	// target. Preserve that behavior when no exact opening origin exists.
	if legacyTarget, legacyErr := e.currentIntegrationToolTarget(ctx, turn); legacyErr == nil {
		destination, destinationErr := e.resolveChannelDestinationForTarget(
			ctx,
			turn,
			legacyTarget.ID,
		)
		if destinationErr == nil && isNativeSlackChannelDestination(destination) {
			return destination, nil
		}
	}
	return channelDestination{}, errMissingChannel
}

func (e Executor) activeSendBindingForOrigin(
	ctx context.Context,
	turn Turn,
	origin integrationstore.IntegrationInputOrigin,
) (integrationstore.IntegrationTargetBindingRecord, error) {
	if origin.BindingID != storage.NilID {
		binding, err := e.Store.Integrations().GetActiveSendBinding(
			ctx,
			turn.ProjectID,
			turn.AgentID,
			origin.BindingID,
		)
		if err == nil && binding.IntegrationTargetID == origin.TargetID {
			return binding, nil
		}
		if err != nil && !errors.Is(err, storeerr.ErrNotFound) {
			return integrationstore.IntegrationTargetBindingRecord{}, err
		}
	}
	return e.Store.Integrations().GetActiveSendBindingForTarget(
		ctx,
		turn.ProjectID,
		turn.AgentID,
		origin.TargetID,
	)
}

func (e Executor) latestChannelOrigin(
	ctx context.Context,
	turn Turn,
) (integrationstore.IntegrationInputOrigin, bool, error) {
	if turn.TurnID != storage.NilID && turn.ModelCallContextID != storage.NilID {
		return e.Store.Integrations().GetLatestModelCallIntegrationOrigin(
			ctx,
			turn.ProjectID,
			turn.AgentID,
			turn.TurnID,
			turn.ModelCallContextID,
		)
	}
	if len(turn.OpeningInputIDs) == 0 {
		return integrationstore.IntegrationInputOrigin{}, false, nil
	}
	return e.Store.Integrations().GetLatestInputIntegrationOrigin(
		ctx,
		turn.ProjectID,
		turn.AgentID,
		turn.OpeningInputIDs,
	)
}

func (e Executor) channelOriginTargets(
	ctx context.Context,
	turn Turn,
) ([]storage.ID, error) {
	if turn.TurnID != storage.NilID && turn.ModelCallContextID != storage.NilID {
		return e.Store.Integrations().ListModelCallIntegrationOriginTargets(
			ctx,
			turn.ProjectID,
			turn.AgentID,
			turn.TurnID,
			turn.ModelCallContextID,
		)
	}
	if len(turn.OpeningInputIDs) == 0 {
		return nil, nil
	}
	return e.Store.Integrations().ListInputIntegrationOriginTargets(
		ctx, turn.ProjectID, turn.AgentID, turn.OpeningInputIDs,
	)
}

func (e Executor) resolveChannelDestinationForTarget(
	ctx context.Context,
	turn Turn,
	targetID storage.ID,
) (channelDestination, error) {
	channelID, err := publicid.Encode(publicid.KindIntegrationTarget, targetID)
	if err != nil {
		return channelDestination{}, err
	}
	return e.resolveChannelDestination(ctx, turn, channelID)
}

func isNativeSlackChannelDestination(destination channelDestination) bool {
	return destination.App.ConnectorKey == "native_slack_v1" &&
		destination.Install.Provider == integrationstore.IntegrationProviderSlack
}

func (e Executor) nativeSlackChannelTarget(
	ctx context.Context,
	turn Turn,
	destination channelDestination,
) (integrationToolTarget, error) {
	if destination.Install.Provider != integrationstore.IntegrationProviderSlack {
		return integrationToolTarget{}, errUnsupportedChannelTransport
	}
	payload, err := e.Store.Secrets().GetProjectOwnedSecretPayload(
		ctx,
		destination.Install.OrgID,
		destination.Install.ProjectID,
		destination.Install.CredentialSecretID,
	)
	if err != nil {
		return integrationToolTarget{}, err
	}
	credentials, err := slack.AppCredentialsFromPayload(payload)
	if err != nil {
		return integrationToolTarget{}, errChannelDisabled
	}
	channelID, err := publicid.Encode(publicid.KindIntegrationTarget, destination.Target.ID)
	if err != nil {
		return integrationToolTarget{}, err
	}
	return integrationToolTarget{
		ID: destination.Target.ID, Provider: destination.Install.Provider,
		PublicID: channelID, TargetRef: destination.Target.TargetRef,
		ProviderRefKind: destination.Target.ProviderRefKind,
		ProviderRef:     destination.Target.ProviderRef, APIToken: credentials.BotToken,
	}, nil
}

func (e Executor) enqueueChannelDelivery(
	ctx context.Context,
	turn Turn,
	record executionstore.ToolCallRecord,
	destination channelDestination,
	text string,
) (integrationstore.IntegrationDeliveryRecord, error) {
	if err := e.ensureIntegrationPostOwnership(ctx, turn); err != nil {
		return integrationstore.IntegrationDeliveryRecord{}, err
	}
	payload, err := channelMessageDeliveryPayload(turn, destination, text, record.ProviderCallID)
	if err != nil {
		return integrationstore.IntegrationDeliveryRecord{}, err
	}
	return e.enqueueConnectorChannelDelivery(
		ctx,
		turn,
		destination,
		connectorChannelDeliveryIntent{
			Kind: "message", PayloadVersion: "channel-message.v1", Payload: payload,
			IdempotencyScope: "send_channel_message/tool_call",
			IdempotencyKey:   record.ID.String(), NotifyRef: record.ID,
		},
	)
}

func channelMessageDeliveryPayload(
	turn Turn,
	destination channelDestination,
	text string,
	providerCallID string,
) (json.RawMessage, error) {
	channelID, err := publicid.Encode(publicid.KindIntegrationTarget, destination.Target.ID)
	if err != nil {
		return nil, err
	}
	agentID, err := publicid.Encode(publicid.KindAgent, turn.AgentID)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(channelOutboundPayloadV1{
		Destination: channelOutboundDestinationV1{
			ChannelID: channelID, ProviderRefKind: destination.Target.ProviderRefKind,
			ProviderRef:      destination.Target.ProviderRef,
			ProviderMetadata: destination.Target.ProviderMetadata,
		},
		Message: channelOutboundMessageV1{Text: text},
		Context: channelOutboundContextV1{
			AgentID:        agentID,
			ProviderCallID: providerCallID,
		},
	})
	if err != nil {
		return nil, err
	}
	return payload, nil
}

func (e Executor) enqueueConnectorChannelDelivery(
	ctx context.Context,
	turn Turn,
	destination channelDestination,
	intent connectorChannelDeliveryIntent,
) (integrationstore.IntegrationDeliveryRecord, error) {
	return e.Store.Integrations().CreateIntegrationDelivery(
		ctx,
		integrationstore.CreateIntegrationDeliveryInput{
			ProjectID: turn.ProjectID, AgentID: turn.AgentID,
			IntegrationTargetBindingID: destination.Binding.ID,
			Transport:                  integrationstore.IntegrationDeliveryTransportConnector,
			DeliveryKind:               intent.Kind, PayloadVersion: intent.PayloadVersion, Payload: intent.Payload,
			IdempotencyScope: intent.IdempotencyScope,
			IdempotencyKey:   intent.IdempotencyKey, NotifyRef: intent.NotifyRef,
		},
	)
}

func (e Executor) waitForChannelDelivery(
	ctx context.Context,
	delivery integrationstore.IntegrationDeliveryRecord,
) (integrationstore.IntegrationDeliveryRecord, error) {
	if channelDeliveryTerminal(delivery.State) || e.IntegrationDeliveries == nil ||
		delivery.NotifyRef == storage.NilID {
		return delivery, nil
	}
	updates := make(chan struct{}, 1)
	subscription, err := e.IntegrationDeliveries.SubscribeIntegrationDeliveryUpdates(
		ctx,
		delivery.NotifyRef,
		func(context.Context) {
			select {
			case updates <- struct{}{}:
			default:
			}
		},
	)
	if err != nil {
		return delivery, nil
	}
	defer func() { _ = subscription.Unsubscribe() }()

	current, err := e.loadChannelDeliveryForWait(ctx, delivery)
	if err != nil {
		return integrationstore.IntegrationDeliveryRecord{}, err
	}
	if channelDeliveryTerminal(current.State) {
		return current, nil
	}
	timer := time.NewTimer(channelDeliveryWait)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			// The provider-visible message is already durably enqueued. Report that
			// accepted state instead of turning local tool-wait cancellation into a
			// failed call that the model may retry and duplicate.
			return current, nil
		case <-timer.C:
			return e.loadChannelDeliveryForWait(ctx, current)
		case <-updates:
			current, err = e.loadChannelDeliveryForWait(ctx, current)
			if err != nil {
				return integrationstore.IntegrationDeliveryRecord{}, err
			}
			if channelDeliveryTerminal(current.State) {
				return current, nil
			}
		}
	}
}

func (e Executor) loadChannelDeliveryForWait(
	ctx context.Context,
	known integrationstore.IntegrationDeliveryRecord,
) (integrationstore.IntegrationDeliveryRecord, error) {
	current, err := e.Store.Integrations().GetIntegrationDelivery(ctx, known.ProjectID, known.ID)
	if err == nil {
		return current, nil
	}
	if ctx.Err() != nil {
		// The delivery was already durably created. Preserve its latest known state
		// when local cancellation races any refresh branch.
		return known, nil //nolint:nilerr // Cancellation changes waiting, not the durable outcome.
	}
	return integrationstore.IntegrationDeliveryRecord{}, err
}

func channelDeliveryTerminal(state integrationstore.IntegrationDeliveryState) bool {
	switch state {
	case integrationstore.IntegrationDeliveryStateDelivered,
		integrationstore.IntegrationDeliveryStateFailed,
		integrationstore.IntegrationDeliveryStateUnknown,
		integrationstore.IntegrationDeliveryStateCanceled:
		return true
	default:
		return false
	}
}

func channelDestinationFailureResult(channelID string, cause error) (toolResultContent, error) {
	code := "channel_error"
	message := cause.Error()
	switch {
	case errors.Is(cause, errMissingChannel):
		code = "missing_channel"
		message = "no active sendable channel is attached to this agent"
	case errors.Is(cause, errAmbiguousChannel):
		code = "ambiguous_channel"
		message = "more than one channel could receive this message; call list_channels and pass channel_id"
	case errors.Is(cause, errChannelDisabled):
		code = "channel_disabled"
		message = "the originating channel is no longer active or sendable"
	case errors.Is(cause, errUnsupportedChannelTransport):
		code = "unsupported_channel_transport"
		message = "this channel transport is not available"
	}
	return structuredToolResultContent(integrationToolResult{
		Code: code, ChannelID: channelID, Message: message,
	})
}

func channelDeliveryToolResult(
	delivery integrationstore.IntegrationDeliveryRecord,
) (asyncPhaseResult, error) {
	channelID, err := publicid.Encode(publicid.KindIntegrationTarget, delivery.IntegrationTargetID)
	if err != nil {
		return nil, err
	}
	deliveryID, err := publicid.Encode(publicid.KindIntegrationDelivery, delivery.ID)
	if err != nil {
		return nil, err
	}
	result := integrationToolResult{
		Provider: delivery.Provider, ChannelID: channelID, DeliveryID: deliveryID,
		ProviderMessageID: delivery.ProviderMessageRef,
	}
	var terminalErr error
	switch delivery.State {
	case integrationstore.IntegrationDeliveryStateDelivered:
		result.Code = "delivered"
	case integrationstore.IntegrationDeliveryStateFailed,
		integrationstore.IntegrationDeliveryStateCanceled:
		result.Code = "delivery_rejected"
		result.Message = channelDeliveryErrorMessage(delivery.LastError, "channel delivery was rejected")
		terminalErr = errors.New(result.Message)
	case integrationstore.IntegrationDeliveryStateUnknown:
		result.Code = "delivery_unknown"
		result.Message = channelDeliveryErrorMessage(
			delivery.LastError,
			"channel delivery may have succeeded, but its outcome could not be confirmed",
		)
		terminalErr = errors.New(result.Message)
	default:
		result.Code = "delivery_pending"
		result.Message = "channel delivery was accepted and is still pending"
	}
	content, err := structuredToolResultContent(result)
	if err != nil {
		return nil, err
	}
	if terminalErr != nil {
		return failAsynchronously(content, terminalErr), nil
	}
	return completeAsynchronously(content), nil
}

func channelDeliveryErrorMessage(raw json.RawMessage, fallback string) string {
	var value struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &value) == nil && strings.TrimSpace(value.Message) != "" {
		return value.Message
	}
	return fallback
}
