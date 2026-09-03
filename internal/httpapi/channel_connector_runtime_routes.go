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

func (s strictOpenAPIServer) ClaimChannelConnectorRuntimeUnits(
	ctx context.Context,
	request openapi.ClaimChannelConnectorRuntimeUnitsRequestObject,
) (openapi.ClaimChannelConnectorRuntimeUnitsResponseObject, error) {
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
	rows, err := s.server.store.Integrations().ClaimIntegrationRuntimeUnits(
		ctx,
		integrationstore.ClaimIntegrationRuntimeUnitsInput{
			LeaseOwner:    scope.ID + ":" + request.Body.Owner,
			LeaseDuration: time.Duration(request.Body.LeaseMs) * time.Millisecond,
			Capability:    capability,
			Limit:         int(request.Body.Limit),
		},
	)
	if err != nil {
		return nil, apierror.FromError(err)
	}
	runtimeUnits := make([]openapi.ChannelConnectorRuntimeUnit, 0, len(rows))
	for _, row := range rows {
		response, err := channelConnectorRuntimeUnitResponse(row)
		if err != nil {
			return nil, err
		}
		runtimeUnits = append(runtimeUnits, response)
	}
	return openapi.ClaimChannelConnectorRuntimeUnits200JSONResponse{RuntimeUnits: runtimeUnits}, nil
}

func (s strictOpenAPIServer) HeartbeatChannelConnectorRuntimeUnit(
	ctx context.Context,
	request openapi.HeartbeatChannelConnectorRuntimeUnitRequestObject,
) (openapi.HeartbeatChannelConnectorRuntimeUnitResponseObject, error) {
	scope, err := channelConnectorScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	unitID, ok := parseOpenAPIPublicID(publicid.KindIntegrationRuntimeUnit, request.RuntimeUnitID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	if (request.Body.CheckpointVersion == nil) != (len(request.Body.Checkpoint) == 0) {
		return nil, apierror.FromCode(
			openapi.ErrorCodeInvalidRequest,
			"checkpoint and checkpoint_version must either both be set or both be omitted",
		)
	}
	checkpointVersion := 0
	if request.Body.CheckpointVersion != nil {
		checkpointVersion = int(*request.Body.CheckpointVersion)
	}
	record, err := s.server.store.Integrations().HeartbeatIntegrationRuntimeUnit(
		ctx,
		integrationstore.HeartbeatIntegrationRuntimeUnitInput{
			ID: unitID, LeaseToken: request.Body.LeaseToken,
			LeaseGeneration:   request.Body.LeaseGeneration,
			LeaseDuration:     time.Duration(request.Body.LeaseMs) * time.Millisecond,
			WriteCheckpoint:   request.Body.CheckpointVersion != nil,
			CheckpointVersion: checkpointVersion, Checkpoint: request.Body.Checkpoint,
			Capabilities: scope.Capabilities,
		},
	)
	if err != nil {
		return nil, apierror.FromError(err)
	}
	response, err := channelConnectorRuntimeUnitResponse(record)
	if err != nil {
		return nil, err
	}
	return openapi.HeartbeatChannelConnectorRuntimeUnit200JSONResponse(response), nil
}

func (s strictOpenAPIServer) ReleaseChannelConnectorRuntimeUnit(
	ctx context.Context,
	request openapi.ReleaseChannelConnectorRuntimeUnitRequestObject,
) (openapi.ReleaseChannelConnectorRuntimeUnitResponseObject, error) {
	scope, err := channelConnectorScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	unitID, ok := parseOpenAPIPublicID(publicid.KindIntegrationRuntimeUnit, request.RuntimeUnitID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	if (request.Body.CheckpointVersion == nil) != (len(request.Body.Checkpoint) == 0) {
		return nil, apierror.FromCode(
			openapi.ErrorCodeInvalidRequest,
			"checkpoint and checkpoint_version must either both be set or both be omitted",
		)
	}
	checkpointVersion := 0
	if request.Body.CheckpointVersion != nil {
		checkpointVersion = int(*request.Body.CheckpointVersion)
	}
	record, err := s.server.store.Integrations().ReleaseIntegrationRuntimeUnit(
		ctx,
		integrationstore.ReleaseIntegrationRuntimeUnitInput{
			ID: unitID, LeaseToken: request.Body.LeaseToken,
			LeaseGeneration:   request.Body.LeaseGeneration,
			WriteCheckpoint:   request.Body.CheckpointVersion != nil,
			CheckpointVersion: checkpointVersion, Checkpoint: request.Body.Checkpoint,
			LastError:    request.Body.LastError,
			Capabilities: scope.Capabilities,
		},
	)
	if err != nil {
		return nil, apierror.FromError(err)
	}
	response, err := channelConnectorRuntimeUnitResponse(record)
	if err != nil {
		return nil, err
	}
	return openapi.ReleaseChannelConnectorRuntimeUnit200JSONResponse(response), nil
}

func channelConnectorRuntimeUnitResponse(
	record integrationstore.IntegrationRuntimeUnitRecord,
) (openapi.ChannelConnectorRuntimeUnit, error) {
	id, err := publicID(publicid.KindIntegrationRuntimeUnit, record.ID)
	if err != nil {
		return openapi.ChannelConnectorRuntimeUnit{}, err
	}
	appID, err := publicID(publicid.KindIntegrationApp, record.IntegrationAppID)
	if err != nil {
		return openapi.ChannelConnectorRuntimeUnit{}, err
	}
	response := openapi.ChannelConnectorRuntimeUnit{
		Id: id, IntegrationAppId: appID, UnitKey: record.UnitKey, RuntimeKind: record.RuntimeKind,
		DesiredState: openapi.ChannelRuntimeDesiredState(record.DesiredState),
		SpecRevision: int32(record.SpecRevision), Configuration: record.Configuration,
		Status: openapi.ChannelRuntimeStatus(record.Status), LeaseGeneration: record.LeaseGeneration,
		CheckpointVersion: int32(record.CheckpointVersion), CheckpointRevision: record.CheckpointRevision,
		Checkpoint: record.Checkpoint, LastError: record.LastError,
		LeasedAt: record.LeasedAt, RenewedAt: record.RenewedAt,
		LeaseExpiresAt: record.LeaseExpiresAt, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}
	if record.IntegrationInstallID != storage.NilID {
		installID, err := publicID(publicid.KindIntegrationInstall, record.IntegrationInstallID)
		if err != nil {
			return openapi.ChannelConnectorRuntimeUnit{}, err
		}
		response.IntegrationInstallId = &installID
	}
	if record.LeaseOwner != "" {
		response.LeaseOwner = &record.LeaseOwner
	}
	if record.LeaseToken != storage.NilID {
		value := record.LeaseToken
		response.LeaseToken = &value
	}
	if record.LeaseSpecRevision > 0 {
		value := int32(record.LeaseSpecRevision)
		response.LeaseSpecRevision = &value
	}
	if record.LeaseAppConfigurationRevision > 0 {
		response.LeaseAppConfigurationRevision = &record.LeaseAppConfigurationRevision
	}
	if record.LeaseInstallConfigRevision > 0 {
		response.LeaseInstallConfigurationRevision = &record.LeaseInstallConfigRevision
	}
	return response, nil
}
