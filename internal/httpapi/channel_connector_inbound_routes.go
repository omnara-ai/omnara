package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/publicid"
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
	response, err := s.receiveChannelConnectorEvent(ctx, scope, appID, *request.Body, nil)
	if err != nil {
		return nil, err
	}
	return openapi.AcceptChannelConnectorEvent202JSONResponse(response), nil
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
	response, err := s.receiveChannelConnectorEvent(ctx, scope, appID, request.Body.Event,
		&integrationstore.IntegrationRuntimeLeaseProof{
			IntegrationAppID: appID, UnitID: unitID,
			LeaseToken: request.Body.LeaseToken, LeaseGeneration: request.Body.LeaseGeneration,
		},
	)
	if err != nil {
		return nil, err
	}
	return openapi.AcceptChannelConnectorRuntimeEvent202JSONResponse(response), nil
}

func (s strictOpenAPIServer) receiveChannelConnectorEvent(
	ctx context.Context,
	scope channelConnectorScope,
	appID uuid.UUID,
	body openapi.ChannelInboundEventRequest,
	runtimeLease *integrationstore.IntegrationRuntimeLeaseProof,
) (openapi.ChannelInboundEventResponse, error) {
	installID, ok := parseOpenAPIPublicID(publicid.KindIntegrationInstall, body.IntegrationInstallId)
	if !ok {
		return openapi.ChannelInboundEventResponse{}, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	install, err := s.channelConnectorEventInstallation(ctx, scope, appID, installID)
	if err != nil {
		return openapi.ChannelInboundEventResponse{}, err
	}
	// The installation lookup establishes scope; the store rechecks live authority
	// and runtime fencing while committing the receipt, before HTTP acknowledgement.
	receipt, err := s.server.store.Integrations().ReceiveIntegrationEvent(ctx,
		integrationstore.ReceiveIntegrationEventInput{
			ProjectID: install.ProjectID, IntegrationInstallID: install.ID,
			EventID: body.EventId, Payload: body.Payload,
			Capabilities: scope.Capabilities, RuntimeLease: runtimeLease,
		},
	)
	if err != nil {
		return openapi.ChannelInboundEventResponse{}, apierror.FromError(err)
	}
	return channelInboundEventResponse(receipt)
}

func (s strictOpenAPIServer) ClaimNextChannelConnectorEvent(
	ctx context.Context,
	request openapi.ClaimNextChannelConnectorEventRequestObject,
) (openapi.ClaimNextChannelConnectorEventResponseObject, error) {
	scope, err := channelConnectorScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	capability, err := scope.authorizeClaimCapability(request.Body.Capability)
	if err != nil {
		return nil, err
	}
	receipt, found, err := s.server.store.Integrations().ClaimNextIntegrationEvent(ctx,
		integrationstore.ClaimNextIntegrationEventInput{
			Capability: capability, LeaseDuration: time.Duration(request.Body.LeaseMs) * time.Millisecond,
		},
	)
	if err != nil {
		return nil, apierror.FromError(err)
	}
	if !found {
		return openapi.ClaimNextChannelConnectorEvent204Response{}, nil
	}
	id, err := publicID(publicid.KindIntegrationEventReceipt, receipt.ID)
	if err != nil {
		return nil, err
	}
	appID, err := publicID(publicid.KindIntegrationApp, receipt.IntegrationAppID)
	if err != nil {
		return nil, err
	}
	installID, err := publicID(publicid.KindIntegrationInstall, receipt.IntegrationInstallID)
	if err != nil {
		return nil, err
	}
	if receipt.LeaseExpiresAt == nil {
		return nil, errors.New("claimed integration event has no lease expiry")
	}
	return openapi.ClaimNextChannelConnectorEvent200JSONResponse{
		ReceiptId: id, IntegrationAppId: appID, IntegrationInstallId: installID,
		EventId: receipt.EventID, Payload: receipt.Payload, State: openapi.ChannelEventState(receipt.State),
		LeaseToken: receipt.LeaseToken, LeaseGeneration: receipt.LeaseGeneration,
		LeaseExpiresAt: *receipt.LeaseExpiresAt, AttemptCount: int32(receipt.AttemptCount),
		LastError: receipt.LastError,
	}, nil
}

func (s strictOpenAPIServer) CompleteChannelConnectorEvent(
	ctx context.Context,
	request openapi.CompleteChannelConnectorEventRequestObject,
) (openapi.CompleteChannelConnectorEventResponseObject, error) {
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
	installID, ok := parseOpenAPIPublicID(publicid.KindIntegrationInstall, request.IntegrationInstallID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	receiptID, ok := parseOpenAPIPublicID(publicid.KindIntegrationEventReceipt, request.ReceiptID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	install, err := s.channelConnectorEventInstallation(ctx, scope, appID, installID)
	if err != nil {
		return nil, err
	}
	receipt, err := s.server.store.Integrations().FinishIntegrationEvent(ctx,
		integrationstore.FinishIntegrationEventInput{
			ProjectID: install.ProjectID, IntegrationInstallID: install.ID, ID: receiptID,
			LeaseToken: request.Body.LeaseToken, LeaseGeneration: request.Body.LeaseGeneration,
			State:     integrationstore.IntegrationEventState(request.Body.State),
			LastError: request.Body.LastError, Capabilities: scope.Capabilities,
		},
	)
	if err != nil {
		return nil, apierror.FromError(err)
	}
	response, err := channelInboundEventResponse(receipt)
	if err != nil {
		return nil, err
	}
	return openapi.CompleteChannelConnectorEvent200JSONResponse(response), nil
}

func (s strictOpenAPIServer) channelConnectorEventInstallation(
	ctx context.Context,
	scope channelConnectorScope,
	appID, installID uuid.UUID,
) (integrationstore.IntegrationInstallRecord, error) {
	if _, err := s.server.store.Integrations().GetConnectorIntegrationApp(ctx, appID, scope.Capabilities); err != nil {
		return integrationstore.IntegrationInstallRecord{}, apierror.FromError(err)
	}
	install, err := s.server.store.Integrations().GetConnectorIntegrationInstallByID(ctx, appID, installID)
	if err != nil {
		return integrationstore.IntegrationInstallRecord{}, apierror.FromError(err)
	}
	return install, nil
}

func channelInboundEventResponse(
	receipt integrationstore.IntegrationEventReceipt,
) (openapi.ChannelInboundEventResponse, error) {
	id, err := publicID(publicid.KindIntegrationEventReceipt, receipt.ID)
	if err != nil {
		return openapi.ChannelInboundEventResponse{}, err
	}
	return openapi.ChannelInboundEventResponse{ReceiptId: id, State: openapi.ChannelEventState(receipt.State)}, nil
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
	appID uuid.UUID,
	interactionID uuid.UUID,
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
		binding.IntegrationTargetID != targetID || !binding.SendAllowed {
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
	if existing.IntegrationTargetID != targetID {
		return openapi.ResolveChannelConnectorInteractionResponse{}, apierror.FromCode(
			openapi.ErrorCodeForbidden, "forbidden",
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
			"actor_metadata": body.Actor.Metadata,
			"metadata":       body.Metadata,
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
