package httpapi

import (
	"context"
	"time"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

func (s strictOpenAPIServer) ClaimChannelConnectorDeliveries(
	ctx context.Context,
	request openapi.ClaimChannelConnectorDeliveriesRequestObject,
) (openapi.ClaimChannelConnectorDeliveriesResponseObject, error) {
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
	rows, err := s.server.store.Integrations().ClaimIntegrationDeliveries(
		ctx,
		integrationstore.ClaimIntegrationDeliveriesInput{
			ClaimedBy:     scope.ID + ":" + request.Body.Owner,
			LeaseDuration: time.Duration(request.Body.LeaseMs) * time.Millisecond,
			Capability:    capability,
			Limit:         int(request.Body.Limit),
		},
	)
	if err != nil {
		return nil, apierror.FromError(err)
	}
	deliveries := make([]openapi.ChannelConnectorDelivery, 0, len(rows))
	for _, row := range rows {
		response, err := channelConnectorDeliveryResponse(row)
		if err != nil {
			return nil, err
		}
		deliveries = append(deliveries, response)
	}
	return openapi.ClaimChannelConnectorDeliveries200JSONResponse{Deliveries: deliveries}, nil
}

func (s strictOpenAPIServer) CompleteChannelConnectorDelivery(
	ctx context.Context,
	request openapi.CompleteChannelConnectorDeliveryRequestObject,
) (openapi.CompleteChannelConnectorDeliveryResponseObject, error) {
	scope, err := channelConnectorScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	deliveryID, ok := parseOpenAPIPublicID(publicid.KindIntegrationDelivery, request.DeliveryID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	retryAfter := time.Duration(0)
	if request.Body.RetryAfterMs != nil {
		retryAfter = time.Duration(*request.Body.RetryAfterMs) * time.Millisecond
	}
	record, err := s.server.store.Integrations().CompleteIntegrationDelivery(
		ctx,
		integrationstore.CompleteIntegrationDeliveryInput{
			ID: deliveryID, ClaimToken: request.Body.ClaimToken,
			ClaimGeneration: request.Body.ClaimGeneration,
			State:           integrationstore.IntegrationDeliveryState(request.Body.Outcome),
			RetryAfter:      retryAfter, ProviderMessageRef: request.Body.ProviderMessageRef,
			LastError: request.Body.LastError, Capabilities: scope.Capabilities,
		},
	)
	if err != nil {
		return nil, apierror.FromError(err)
	}
	if record.NotifyRef != storage.NilID && s.server.integrationDeliveryPublisher != nil {
		if publishErr := s.server.integrationDeliveryPublisher.PublishIntegrationDeliveryUpdate(
			ctx,
			record.NotifyRef,
		); publishErr != nil {
			s.server.log.ErrorContext(
				ctx,
				"publish integration delivery update",
				"delivery_id", record.ID,
				"error", publishErr,
			)
		}
	}
	response, err := channelConnectorDeliveryResponse(record)
	if err != nil {
		return nil, err
	}
	return openapi.CompleteChannelConnectorDelivery200JSONResponse(response), nil
}

func channelConnectorDeliveryResponse(
	record integrationstore.IntegrationDeliveryRecord,
) (openapi.ChannelConnectorDelivery, error) {
	id, err := publicID(publicid.KindIntegrationDelivery, record.ID)
	if err != nil {
		return openapi.ChannelConnectorDelivery{}, err
	}
	appID, err := publicID(publicid.KindIntegrationApp, record.IntegrationAppID)
	if err != nil {
		return openapi.ChannelConnectorDelivery{}, err
	}
	installID, err := publicID(publicid.KindIntegrationInstall, record.IntegrationInstallID)
	if err != nil {
		return openapi.ChannelConnectorDelivery{}, err
	}
	targetID, err := publicID(publicid.KindIntegrationTarget, record.IntegrationTargetID)
	if err != nil {
		return openapi.ChannelConnectorDelivery{}, err
	}
	bindingID, err := publicID(publicid.KindIntegrationBinding, record.IntegrationTargetBindingID)
	if err != nil {
		return openapi.ChannelConnectorDelivery{}, err
	}
	response := openapi.ChannelConnectorDelivery{
		Id: id, IntegrationAppId: appID, IntegrationInstallId: installID,
		IntegrationTargetId: targetID, IntegrationTargetBindingId: bindingID,
		Provider: record.Provider, ConnectorKey: record.ConnectorKey,
		DeliveryKind: record.DeliveryKind, PayloadVersion: record.PayloadVersion,
		Payload: record.Payload, State: openapi.ChannelDeliveryState(record.State),
		AttemptCount: int32(record.AttemptCount), AvailableAt: record.AvailableAt,
		ClaimGeneration: record.ClaimGeneration, LastError: record.LastError,
		CompletedAt: record.CompletedAt, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}
	if record.ClaimToken != storage.NilID {
		value := record.ClaimToken
		response.ClaimToken = &value
	}
	response.ClaimExpiresAt = record.ClaimExpiresAt
	if record.NotifyRef != storage.NilID {
		value := record.NotifyRef
		response.NotifyRef = &value
	}
	if record.ProviderMessageRef != "" {
		response.ProviderMessageRef = &record.ProviderMessageRef
	}
	if record.AppConfigurationRevision > 0 {
		response.AppConfigurationRevision = &record.AppConfigurationRevision
	}
	if record.InstallConfigurationRevision > 0 {
		response.InstallConfigurationRevision = &record.InstallConfigurationRevision
	}
	return response, nil
}
