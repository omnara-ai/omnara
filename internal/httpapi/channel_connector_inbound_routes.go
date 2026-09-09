package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/integration"
	"github.com/omnara-ai/omnara/internal/interactionform"
	logpkg "github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

func (s strictOpenAPIServer) AcceptChannelConnectorEvent(
	ctx context.Context,
	request openapi.AcceptChannelConnectorEventRequestObject,
) (openapi.AcceptChannelConnectorEventResponseObject, error) {
	scope, err := channelConnectorScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	appID, ok := parseOpenAPIPublicID(publicid.KindIntegrationApp, request.IntegrationAppID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	envelope, err := channelInboundEnvelope(*request.Body)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	response, err := s.processChannelInbound(ctx, scope, appID, envelope, nil)
	if err != nil {
		return nil, err
	}
	return openapi.AcceptChannelConnectorEvent200JSONResponse(response), nil
}

func (s strictOpenAPIServer) AcceptChannelConnectorRuntimeEvent(
	ctx context.Context,
	request openapi.AcceptChannelConnectorRuntimeEventRequestObject,
) (openapi.AcceptChannelConnectorRuntimeEventResponseObject, error) {
	scope, err := channelConnectorScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	appID, ok := parseOpenAPIPublicID(publicid.KindIntegrationApp, request.IntegrationAppID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	unitID, ok := parseOpenAPIPublicID(publicid.KindIntegrationRuntimeUnit, request.RuntimeUnitID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	envelope, err := channelInboundEnvelope(request.Body.Event)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	lease := integration.ChannelRuntimeLease{
		UnitID: unitID, Token: request.Body.LeaseToken, Generation: request.Body.LeaseGeneration,
	}
	response, err := s.processChannelInbound(ctx, scope, appID, envelope, &lease)
	if err != nil {
		return nil, err
	}
	return openapi.AcceptChannelConnectorRuntimeEvent200JSONResponse(response), nil
}

func (s strictOpenAPIServer) processChannelInbound(
	ctx context.Context,
	scope channelConnectorScope,
	appID integrationstore.ID,
	envelope integration.ChannelInboundEnvelope,
	lease *integration.ChannelRuntimeLease,
) (openapi.ChannelInboundEventResponse, error) {
	input := integration.ProcessChannelInboundInput{
		IntegrationAppID: appID, Capabilities: scope.Capabilities,
		Envelope: envelope,
		PrepareContent: func(
			_ context.Context,
			contentBlocks json.RawMessage,
		) (integration.MaterializeChannelInboundContentFunc, error) {
			contentPlan, err := preflightInlineMedia(
				contentBlocks,
				inlineMediaAgentInput,
				maxContentBlocksPerInput,
			)
			if err != nil {
				return nil, err
			}
			return func(
				ctx context.Context,
				input integration.MaterializeChannelInboundContentInput,
			) (json.RawMessage, error) {
				return s.server.materializeInlineMedia(ctx, mediaIngestContext{
					ProjectID: input.ProjectID, AgentID: input.AgentID,
					IntegrationInstallID: input.IntegrationInstallID,
					IdempotencyKey:       input.IdempotencyKey,
					RuntimeLease:         input.RuntimeLease,
				}, contentPlan)
			}, nil
		},
	}
	var (
		result integration.ProcessChannelInboundResult
		err    error
	)
	if lease == nil {
		result, err = s.server.channels.ProcessInbound(ctx, input)
	} else {
		result, err = s.server.channels.ProcessRuntimeInbound(ctx, input, *lease)
	}
	if err != nil {
		return openapi.ChannelInboundEventResponse{}, channelInboundProcessError(ctx, err)
	}
	for _, failure := range result.FailedRoutes {
		logpkg.LoggerFromContext(ctx).WarnContext(
			ctx,
			"channel inbound route rejected a permanent configuration error",
			"integration_route_id",
			failure.RouteID,
			"error",
			failure.Err,
		)
	}
	accepted := make([]openapi.ChannelInboundAcceptance, 0, len(result.Accepted))
	for _, item := range result.Accepted {
		response, err := channelInboundAcceptanceResponse(item)
		if err != nil {
			return openapi.ChannelInboundEventResponse{}, err
		}
		accepted = append(accepted, response)
		if item.Launch.Agent.ID != storage.NilID {
			s.server.startLaunchMachineProvisioning(ctx, logpkg.LoggerFromContext(ctx), item.Launch)
		}
	}
	return openapi.ChannelInboundEventResponse{
		Accepted: accepted, IgnoredRoutes: int32(result.IgnoredRoutes),
	}, nil
}

func channelInboundProcessError(ctx context.Context, err error) error {
	if errors.Is(err, integration.ErrChannelRouteHandlerUnavailable) {
		logpkg.LoggerFromContext(ctx).WarnContext(
			ctx,
			"channel inbound route handler is temporarily unavailable",
			"error",
			err,
		)
		return apierror.FromCode(
			openapi.ErrorCodeServiceUnavailable,
			"channel inbound route handler is temporarily unavailable",
		)
	}
	if errors.Is(err, integration.ErrChannelInboundCompletionRetry) {
		logpkg.LoggerFromContext(ctx).WarnContext(
			ctx,
			"channel inbound completion will be retried",
			"error",
			err,
		)
		return apierror.FromCode(
			openapi.ErrorCodeServiceUnavailable,
			"channel inbound completion is temporarily unavailable",
		)
	}
	var mediaErr mediaIngestError
	if errors.As(err, &mediaErr) {
		return mediaIngestAPIError(err)
	}
	return apierror.FromError(err)
}

func (s strictOpenAPIServer) ResolveChannelConnectorInteraction(
	ctx context.Context,
	request openapi.ResolveChannelConnectorInteractionRequestObject,
) (openapi.ResolveChannelConnectorInteractionResponseObject, error) {
	scope, err := channelConnectorScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	appID, ok := parseOpenAPIPublicID(publicid.KindIntegrationApp, request.IntegrationAppID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	interactionID, ok := parseOpenAPIPublicID(
		publicid.KindAgentInteraction,
		request.InteractionID,
	)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	response, err := s.resolveChannelConnectorInteraction(
		ctx,
		scope,
		appID,
		interactionID,
		*request.Body,
		nil,
	)
	if err != nil {
		return nil, err
	}
	return openapi.ResolveChannelConnectorInteraction200JSONResponse(response), nil
}

func (s strictOpenAPIServer) ResolveChannelConnectorRuntimeInteraction(
	ctx context.Context,
	request openapi.ResolveChannelConnectorRuntimeInteractionRequestObject,
) (openapi.ResolveChannelConnectorRuntimeInteractionResponseObject, error) {
	scope, err := channelConnectorScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	appID, ok := parseOpenAPIPublicID(publicid.KindIntegrationApp, request.IntegrationAppID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	unitID, ok := parseOpenAPIPublicID(
		publicid.KindIntegrationRuntimeUnit,
		request.RuntimeUnitID,
	)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	interactionID, ok := parseOpenAPIPublicID(
		publicid.KindAgentInteraction,
		request.InteractionID,
	)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	response, err := s.resolveChannelConnectorInteraction(
		ctx,
		scope,
		appID,
		interactionID,
		request.Body.Interaction,
		&executionstore.IntegrationRuntimeLeaseProof{
			IntegrationAppID: appID,
			UnitID:           unitID,
			LeaseToken:       request.Body.LeaseToken,
			LeaseGeneration:  request.Body.LeaseGeneration,
		},
	)
	if err != nil {
		return nil, err
	}
	return openapi.ResolveChannelConnectorRuntimeInteraction200JSONResponse(response), nil
}

func (s strictOpenAPIServer) resolveChannelConnectorInteraction(
	ctx context.Context,
	scope channelConnectorScope,
	appID integrationstore.ID,
	interactionID executionstore.ID,
	body openapi.ResolveChannelConnectorInteractionRequest,
	runtimeLease *executionstore.IntegrationRuntimeLeaseProof,
) (openapi.ResolveChannelConnectorInteractionResponse, error) {
	body, metadata, err := normalizeChannelInteractionRequest(body)
	if err != nil {
		return openapi.ResolveChannelConnectorInteractionResponse{}, apierror.FromCode(
			openapi.ErrorCodeInvalidRequest,
			err.Error(),
		)
	}
	targetID, ok := parseOpenAPIPublicID(
		publicid.KindIntegrationTarget,
		body.IntegrationTargetId,
	)
	if !ok {
		return openapi.ResolveChannelConnectorInteractionResponse{}, apierror.FromCode(
			openapi.ErrorCodeNotFound,
			"not found",
		)
	}
	bindingID, ok := parseOpenAPIPublicID(
		publicid.KindIntegrationBinding,
		body.IntegrationTargetBindingId,
	)
	if !ok {
		return openapi.ResolveChannelConnectorInteractionResponse{}, apierror.FromCode(
			openapi.ErrorCodeNotFound,
			"not found",
		)
	}
	if _, err := s.server.store.Integrations().GetConnectorIntegrationApp(
		ctx,
		appID,
		scope.Capabilities,
	); err != nil {
		return openapi.ResolveChannelConnectorInteractionResponse{}, apierror.FromError(err)
	}
	install, err := s.server.store.Integrations().GetConnectorIntegrationInstall(
		ctx,
		appID,
		body.ExternalTenantId,
		body.ExternalAccountRef,
	)
	if err != nil {
		return openapi.ResolveChannelConnectorInteractionResponse{}, apierror.FromError(err)
	}
	binding, err := s.server.store.Integrations().GetIntegrationTargetBinding(
		ctx,
		install.ProjectID,
		bindingID,
	)
	if err != nil {
		return openapi.ResolveChannelConnectorInteractionResponse{}, apierror.FromError(err)
	}
	if binding.IntegrationInstallID != install.ID ||
		binding.IntegrationTargetID != targetID || !binding.ReceiveAllowed {
		return openapi.ResolveChannelConnectorInteractionResponse{}, apierror.FromCode(
			openapi.ErrorCodeForbidden,
			"forbidden",
		)
	}
	target, err := s.server.store.Integrations().GetIntegrationTarget(
		ctx,
		install.ProjectID,
		targetID,
	)
	if err != nil {
		return openapi.ResolveChannelConnectorInteractionResponse{}, apierror.FromError(err)
	}
	if target.IntegrationInstallID != install.ID {
		return openapi.ResolveChannelConnectorInteractionResponse{}, apierror.FromCode(
			openapi.ErrorCodeForbidden,
			"forbidden",
		)
	}
	existing, found, err := s.server.store.Execution().GetAgentInteraction(
		ctx,
		install.ProjectID,
		binding.AgentID,
		interactionID,
	)
	if err != nil {
		return openapi.ResolveChannelConnectorInteractionResponse{}, apierror.FromError(err)
	}
	if !found {
		return openapi.ResolveChannelConnectorInteractionResponse{}, apierror.FromCode(
			openapi.ErrorCodeNotFound,
			"not found",
		)
	}
	resolution, err := channelInteractionResolution(existing, body.Answers)
	if err != nil {
		return openapi.ResolveChannelConnectorInteractionResponse{}, apierror.FromCode(
			openapi.ErrorCodeInvalidRequest,
			err.Error(),
		)
	}
	displayName := body.Actor.DisplayName
	resolved, err := s.server.store.Execution().ResolveAgentInteraction(
		ctx,
		executionstore.ResolveAgentInteractionInput{
			ProjectID: install.ProjectID, AgentID: binding.AgentID, ID: interactionID,
			Resolution: resolution,
			Actor: &executionstore.ActorParams{
				Provider: install.Provider, ProviderTenantID: install.ProviderTenantID,
				ProviderUserID: body.Actor.Ref, DisplayName: &displayName,
			},
			IntegrationTargetID: targetID, IntegrationTargetBindingID: binding.ID,
			IntegrationInstallID: install.ID, Metadata: metadata, RuntimeLease: runtimeLease,
		},
	)
	if err != nil {
		return openapi.ResolveChannelConnectorInteractionResponse{}, apierror.FromError(err)
	}
	status := "resolved"
	if resolved.Replayed {
		status = "already_resolved"
	}
	return resolvedChannelInteractionResponse(status, existing, &resolution), nil
}

func channelInteractionResolution(
	record executionstore.AgentInteractionRecord,
	answers []openapi.InteractionAnswer,
) (interactionform.Resolution, error) {
	return publicInteractionResolution(record, answers)
}

func channelInteractionResponseMetadata(
	body openapi.ResolveChannelConnectorInteractionRequest,
) (json.RawMessage, error) {
	metadata, err := json.Marshal(map[string]any{
		"channel": map[string]any{
			"version": body.Version, "actor_metadata": body.Actor.Metadata,
			"metadata": body.Metadata,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("encode channel interaction metadata: %w", err)
	}
	return metadata, nil
}

func normalizeChannelInteractionRequest(
	body openapi.ResolveChannelConnectorInteractionRequest,
) (openapi.ResolveChannelConnectorInteractionRequest, json.RawMessage, error) {
	if string(body.Version) != integration.ChannelEnvelopeVersionV1 {
		return body, nil, fmt.Errorf("unsupported channel envelope version %q", body.Version)
	}
	if strings.TrimSpace(body.ExternalAccountRef) == "" || strings.TrimSpace(body.Actor.Ref) == "" {
		return body, nil, errors.New("external account and actor refs are required")
	}
	if len(body.ExternalTenantId) > 512 || len(body.ExternalAccountRef) > 512 ||
		len(body.Actor.Ref) > 512 {
		return body, nil, errors.New("channel interaction identifier exceeds its size limit")
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"external tenant ID", body.ExternalTenantId},
		{"external account ref", body.ExternalAccountRef},
		{"actor ref", body.Actor.Ref},
		{"actor display name", body.Actor.DisplayName},
	} {
		if err := dbsafe.Text(field.value); err != nil {
			return body, nil, fmt.Errorf("channel interaction %s %w", field.name, err)
		}
	}
	if utf8.RuneCountInString(body.Actor.DisplayName) > executionstore.MaxActorDisplayNameLength {
		return body, nil, fmt.Errorf(
			"channel interaction actor display name exceeds %d characters",
			executionstore.MaxActorDisplayNameLength,
		)
	}
	actorMetadata, err := channelconnector.NormalizeOpaqueObject(body.Actor.Metadata)
	if err != nil {
		return body, nil, fmt.Errorf("channel interaction actor metadata: %w", err)
	}
	requestMetadata, err := channelconnector.NormalizeOpaqueObject(body.Metadata)
	if err != nil {
		return body, nil, fmt.Errorf("channel interaction metadata: %w", err)
	}
	body.Actor.Metadata = actorMetadata
	body.Metadata = requestMetadata
	metadata, err := channelInteractionResponseMetadata(body)
	if err != nil {
		return body, nil, err
	}
	if len(metadata) > channelconnector.MaxMetadataBytes {
		return body, nil, fmt.Errorf(
			"channel interaction response metadata exceeds the %d-byte limit",
			channelconnector.MaxMetadataBytes,
		)
	}
	if err := dbsafe.JSONB(metadata, channelconnector.MaxMetadataBytes); err != nil {
		return body, nil, fmt.Errorf(
			"channel interaction response metadata PostgreSQL-safe JSON: %w",
			err,
		)
	}
	return body, metadata, nil
}

func resolvedChannelInteractionResponse(
	status string,
	record executionstore.AgentInteractionRecord,
	resolution *interactionform.Resolution,
) openapi.ResolveChannelConnectorInteractionResponse {
	text := "This prompt has already been resolved."
	if resolution != nil {
		text = integrationActionResolvedText(record, *resolution)
	}
	return openapi.ResolveChannelConnectorInteractionResponse{
		Status: openapi.ResolveChannelConnectorInteractionResponseStatus(status),
		Text:   text,
	}
}

func channelInboundEnvelope(body openapi.ChannelInboundEventRequest) (integration.ChannelInboundEnvelope, error) {
	contentBlocks, err := rawJSONFromContentBlocks(body.ContentBlocks)
	if err != nil {
		return integration.ChannelInboundEnvelope{}, err
	}
	envelope := integration.ChannelInboundEnvelope{
		Version: string(body.Version), ProviderEventID: body.ProviderEventId,
		ExternalTenantID: body.ExternalTenantId, ExternalAccountRef: body.ExternalAccountRef,
		EventType: body.EventType, ContentBlocks: contentBlocks, OccurredAt: body.OccurredAt,
		Metadata: body.Metadata,
		Conversation: integration.ChannelConversation{
			Ref: body.Conversation.Ref, Kind: body.Conversation.Kind,
			DisplayName: stringFromPtr(body.Conversation.DisplayName),
			ParentRef:   stringFromPtr(body.Conversation.ParentRef),
			ReplyToRef:  stringFromPtr(body.Conversation.ReplyToRef),
			Mentioned:   body.Conversation.Mentioned, Direct: body.Conversation.Direct,
			Metadata: body.Conversation.Metadata,
		},
		Actor: integration.ChannelActor{
			Ref: body.Actor.Ref, DisplayName: body.Actor.DisplayName, Metadata: body.Actor.Metadata,
		},
	}
	return envelope, nil
}

func channelInboundAcceptanceResponse(
	item integration.ChannelInboundAcceptance,
) (openapi.ChannelInboundAcceptance, error) {
	routeID, err := publicID(publicid.KindIntegrationRoute, item.RouteID)
	if err != nil {
		return openapi.ChannelInboundAcceptance{}, err
	}
	agentID, err := publicID(publicid.KindAgent, item.AgentID)
	if err != nil {
		return openapi.ChannelInboundAcceptance{}, err
	}
	targetID, err := publicID(publicid.KindIntegrationTarget, item.TargetID)
	if err != nil {
		return openapi.ChannelInboundAcceptance{}, err
	}
	bindingID, err := publicID(publicid.KindIntegrationBinding, item.BindingID)
	if err != nil {
		return openapi.ChannelInboundAcceptance{}, err
	}
	agentInputID, err := publicID(publicid.KindAgentInput, item.AgentInputID)
	if err != nil {
		return openapi.ChannelInboundAcceptance{}, err
	}
	return openapi.ChannelInboundAcceptance{
		RouteId: routeID, AgentId: agentID, TargetId: targetID,
		BindingId: bindingID, AgentInputId: agentInputID,
	}, nil
}
