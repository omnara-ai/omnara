package httpapi

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

func (s strictOpenAPIServer) AcceptChannelConnectorControlEvent(
	ctx context.Context, request openapi.AcceptChannelConnectorControlEventRequestObject,
) (openapi.AcceptChannelConnectorControlEventResponseObject, error) {
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
	receipt, err := s.server.store.Integrations().ReceiveIntegrationControl(ctx,
		integrationstore.ReceiveIntegrationControlInput{
			IntegrationAppID: appID, ProviderTenantID: request.Body.ProviderTenantId,
			EventID: request.Body.EventId, Payload: request.Body.Payload, Capabilities: scope.Capabilities,
		})
	if err != nil {
		return nil, apierror.FromError(err)
	}
	response, err := channelInboundControlResponse(receipt)
	if err != nil {
		return nil, err
	}
	return openapi.AcceptChannelConnectorControlEvent202JSONResponse(response), nil
}

func (s strictOpenAPIServer) ClaimNextChannelConnectorControlEvent(
	ctx context.Context, request openapi.ClaimNextChannelConnectorControlEventRequestObject,
) (openapi.ClaimNextChannelConnectorControlEventResponseObject, error) {
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
	receipt, found, err := s.server.store.Integrations().ClaimNextIntegrationControl(ctx,
		integrationstore.ClaimNextIntegrationControlInput{
			Capability: capability, LeaseDuration: time.Duration(request.Body.LeaseMs) * time.Millisecond,
		})
	if err != nil {
		return nil, apierror.FromError(err)
	}
	if !found {
		return openapi.ClaimNextChannelConnectorControlEvent204Response{}, nil
	}
	base, err := channelInboundControlResponse(receipt)
	if err != nil {
		return nil, err
	}
	appID, err := publicID(publicid.KindIntegrationApp, receipt.IntegrationAppID)
	if err != nil {
		return nil, err
	}
	if receipt.LeaseExpiresAt == nil {
		return nil, errors.New("claimed integration control receipt has no lease expiry")
	}
	return openapi.ClaimNextChannelConnectorControlEvent200JSONResponse{
		ReceiptId: base.ReceiptId, IntegrationAppId: appID, ProviderTenantId: receipt.ProviderTenantID,
		EventId: receipt.EventID, Payload: receipt.Payload, State: base.State,
		LastInstallationId: base.LastInstallationId, EndInstallationId: base.EndInstallationId,
		LeaseToken: receipt.LeaseToken, LeaseGeneration: receipt.LeaseGeneration,
		LeaseExpiresAt: *receipt.LeaseExpiresAt, AttemptsSinceProgress: receipt.AttemptsSinceProgress,
		LastError: receipt.LastError, CreatedAt: receipt.CreatedAt,
	}, nil
}

func (s strictOpenAPIServer) CompleteChannelConnectorControlEvent(
	ctx context.Context, request openapi.CompleteChannelConnectorControlEventRequestObject,
) (openapi.CompleteChannelConnectorControlEventResponseObject, error) {
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
	receiptID, ok := parseOpenAPIPublicID(publicid.KindIntegrationControlReceipt, request.ReceiptID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	lastID, err := optionalControlInstallationID(request.Body.LastInstallationId)
	if err != nil {
		return nil, err
	}
	var retryAfter time.Duration
	if request.Body.RetryAfterMs != nil {
		if *request.Body.RetryAfterMs < 0 || *request.Body.RetryAfterMs > 86_400_000 {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid retry delay")
		}
		retryAfter = time.Duration(*request.Body.RetryAfterMs) * time.Millisecond
	}
	receipt, err := s.server.store.Integrations().FinishIntegrationControl(ctx,
		integrationstore.FinishIntegrationControlInput{
			IntegrationAppID: appID, ID: receiptID, LeaseToken: request.Body.LeaseToken,
			LeaseGeneration: request.Body.LeaseGeneration, LastInstallID: lastID,
			Outcome: integrationstore.IntegrationControlOutcome(request.Body.Outcome), RetryAfter: retryAfter,
			LastError: request.Body.LastError, Capabilities: scope.Capabilities,
		})
	if err != nil {
		return nil, apierror.FromError(err)
	}
	response, err := channelInboundControlResponse(receipt)
	if err != nil {
		return nil, err
	}
	return openapi.CompleteChannelConnectorControlEvent200JSONResponse(response), nil
}

func channelInboundControlResponse(
	receipt integrationstore.IntegrationControlReceipt,
) (openapi.ChannelInboundControlEventResponse, error) {
	id, err := publicID(publicid.KindIntegrationControlReceipt, receipt.ID)
	if err != nil {
		return openapi.ChannelInboundControlEventResponse{}, err
	}
	last, err := idOrNil(publicid.KindIntegrationInstall, receipt.Progress.LastInstallID)
	if err != nil {
		return openapi.ChannelInboundControlEventResponse{}, err
	}
	end, err := idOrNil(publicid.KindIntegrationInstall, receipt.Progress.EndInstallID)
	if err != nil {
		return openapi.ChannelInboundControlEventResponse{}, err
	}
	return openapi.ChannelInboundControlEventResponse{
		ReceiptId: id, State: openapi.ChannelEventState(receipt.State),
		LastInstallationId: nullableFromPtr(last), EndInstallationId: nullableFromPtr(end),
	}, nil
}

func optionalControlInstallationID(raw *string) (*uuid.UUID, error) {
	if raw == nil {
		return nil, nil //nolint:nilnil // An omitted optional scan boundary is valid.
	}
	id, ok := parseOpenAPIPublicID(publicid.KindIntegrationInstall, *raw)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid control installation ID")
	}
	return &id, nil
}
