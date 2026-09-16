package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

func (s strictOpenAPIServer) RegisterChannel(
	ctx context.Context, request openapi.RegisterChannelRequestObject,
) (openapi.RegisterChannelResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	installID, ok := parseOpenAPIPublicID(publicid.KindIntegrationInstall, request.IntegrationInstallID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	body, err := request.Body.ValueByDiscriminator()
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "channel source must be external or managed")
	}
	var channel integrationstore.IntegrationTargetRecord
	switch body := body.(type) {
	case openapi.RegisterExternalChannelRequest:
		channel, err = s.registerExternalChannel(ctx, scope.project.ID, installID, body)
	case openapi.RegisterManagedChannelRequest:
		channel, err = s.registerManagedChannel(ctx, scope.project.ID, installID, body)
	default:
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "channel source must be external or managed")
	}
	if err != nil {
		return nil, err
	}
	response, err := publicRegisteredChannel(integrationstore.RegisteredChannel{
		ID: channel.ID, ChannelDefinitionID: channel.ChannelDefinitionID, ParentChannelID: channel.ParentChannelID,
		Name: channel.DisplayName, ProviderRef: channel.ProviderRef, ProviderRefKind: channel.ProviderRefKind,
	})
	return openapi.RegisterChannel200JSONResponse(response), err
}

func (s strictOpenAPIServer) registerExternalChannel(
	ctx context.Context, projectID, installID uuid.UUID, body openapi.RegisterExternalChannelRequest,
) (integrationstore.IntegrationTargetRecord, error) {
	definitionID, ok := parseOpenAPIPublicID(publicid.KindChannelDefinition, body.DefinitionId)
	if !ok {
		return integrationstore.IntegrationTargetRecord{}, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	parentID := uuid.Nil
	if body.ParentChannelId != nil {
		parentID, ok = parseOpenAPIPublicID(publicid.KindIntegrationTarget, *body.ParentChannelId)
		if !ok {
			return integrationstore.IntegrationTargetRecord{}, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
		}
	}
	input := integrationstore.CreateIntegrationTargetInput{
		ProjectID: projectID, IntegrationInstallID: installID, ChannelDefinitionID: definitionID,
		ParentChannelID: parentID, ProviderRef: body.ProviderRef, ProviderRefKind: body.ProviderRefKind,
		DisplayName: body.Name, ProviderMetadata: body.ProviderMetadata,
	}
	channel, err := s.server.store.Integrations().RegisterExternalChannel(ctx, input)
	if err != nil {
		return channel, apierror.ProjectScoped(err)
	}
	return channel, nil
}

func (s strictOpenAPIServer) registerManagedChannel(
	ctx context.Context, projectID, installID uuid.UUID, body openapi.RegisterManagedChannelRequest,
) (integrationstore.IntegrationTargetRecord, error) {
	var empty integrationstore.IntegrationTargetRecord
	if err := channelAddressText("provider ref", body.ProviderRef, 512); err != nil {
		return empty, err
	}
	if body.ProviderRefKind != nil {
		if err := channelAddressText("provider ref kind", *body.ProviderRefKind, 128); err != nil {
			return empty, err
		}
	}
	install, err := s.server.store.Integrations().GetIntegrationInstall(ctx, projectID, installID)
	if err != nil {
		return empty, apierror.ProjectScoped(err)
	}
	if install.IntegrationKind != integrationstore.IntegrationKindManaged ||
		install.State != integrationstore.IntegrationInstallStateActive {
		return empty, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	app, err := s.server.store.Integrations().GetIntegrationApp(ctx, install.OrgID, install.IntegrationAppID)
	if err != nil {
		return empty, apierror.ProjectScoped(err)
	}
	if app.State != integrationstore.IntegrationAppStateActive {
		return empty, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	payload, err := json.Marshal(openapi.ChannelResolveAddressOperation{
		ProviderRef: body.ProviderRef, ProviderRefKind: body.ProviderRefKind,
	})
	if err != nil {
		return empty, err
	}
	projectRef, err := publicID(publicid.KindProject, projectID)
	if err != nil {
		return empty, err
	}
	appRef, err := publicID(publicid.KindIntegrationApp, app.ID)
	if err != nil {
		return empty, err
	}
	installRef, err := publicID(publicid.KindIntegrationInstall, installID)
	if err != nil {
		return empty, err
	}
	// No database locks span this RPC. The resolver may publish definitions back
	// to core; registration rechecks live authority in its own transaction.
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	result, err := s.server.channelOperations.Execute(ctx, channelconnector.OperationRequest{
		RequestID: uuid.NewString(), Kind: channelconnector.OperationResolveAddress,
		Capability: channelconnector.Capability{ConnectorKey: app.ConnectorKey, Provider: app.Provider},
		Scope: channelconnector.OperationScope{
			ProjectID: projectRef, IntegrationAppID: appRef, IntegrationInstallID: installRef,
		}, Payload: payload,
	})
	if err != nil || result.Outcome != channelconnector.OperationCompleted {
		if result.Outcome == channelconnector.OperationFailed {
			if failure, decodeErr := channelconnector.DecodeOperationFailure(result.Payload); decodeErr == nil {
				if failure.Code == channelconnector.FailureInvalidAddress {
					return empty, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid channel address")
				}
				if failure.Code == channelconnector.FailureUnsupportedAddress {
					return empty, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "channel address type is not supported")
				}
				if failure.Code == channelconnector.FailureAddressUnavailable {
					return empty, apierror.FromCode(openapi.ErrorCodeNotFound, "channel address is not available to this installation")
				}
			}
		}
		return empty, apierror.FromCode(openapi.ErrorCodeServiceUnavailable, "channel address could not be resolved")
	}
	address, err := decodeChannelRegistrationTarget(result.Payload)
	if err != nil {
		return empty, apierror.FromCode(openapi.ErrorCodeServiceUnavailable, "invalid channel address response")
	}
	target, parent, err := channelRegistrationInput(address)
	if err != nil {
		return empty, apierror.FromCode(openapi.ErrorCodeServiceUnavailable, "invalid channel address response")
	}
	target.ProjectID, target.IntegrationInstallID = projectID, installID
	channel, err := s.server.store.Integrations().RegisterManagedChannel(ctx, target, parent)
	if err != nil {
		return empty, apierror.ProjectScoped(err)
	}
	return channel, nil
}

func channelRegistrationInput(
	body openapi.ChannelRegistrationTarget,
) (integrationstore.CreateIntegrationTargetInput, *integrationstore.CreateIntegrationTargetInput, error) {
	var empty integrationstore.CreateIntegrationTargetInput
	if body.Parent != nil && body.ParentChannelId != nil {
		return empty, nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "supply either parent or parent_channel_id")
	}
	target, err := channelRegistrationAddress(openapi.ChannelRegistrationParent{
		DefinitionId: body.DefinitionId, ProviderRef: body.ProviderRef, ProviderRefKind: body.ProviderRefKind,
		DisplayName: body.DisplayName, ProviderMetadata: body.ProviderMetadata,
	})
	if err != nil {
		return empty, nil, err
	}
	if body.ParentChannelId != nil {
		id, ok := parseOpenAPIPublicID(publicid.KindIntegrationTarget, *body.ParentChannelId)
		if !ok {
			return empty, nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
		}
		target.ParentChannelID = id
	}
	var parent *integrationstore.CreateIntegrationTargetInput
	if body.Parent != nil {
		value, err := channelRegistrationAddress(*body.Parent)
		if err != nil {
			return empty, nil, err
		}
		parent = &value
	}
	return target, parent, nil
}

// Both addresses use the same validation; inline parentage only changes their
// atomic registration order, never the caller's project or installation scope.
func channelRegistrationAddress(
	body openapi.ChannelRegistrationParent,
) (integrationstore.CreateIntegrationTargetInput, error) {
	input := integrationstore.CreateIntegrationTargetInput{}
	definitionID, ok := parseOpenAPIPublicID(publicid.KindChannelDefinition, body.DefinitionId)
	if !ok {
		return input, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	input.ChannelDefinitionID = definitionID
	for _, field := range []struct {
		name, value string
		maxBytes    int
	}{
		{"provider ref", body.ProviderRef, 512},
		{"provider ref kind", body.ProviderRefKind, 128},
	} {
		if err := channelAddressText(field.name, field.value, field.maxBytes); err != nil {
			return input, err
		}
	}
	input.ProviderRef, input.ProviderRefKind = body.ProviderRef, body.ProviderRefKind
	if body.DisplayName != nil {
		input.DisplayName = *body.DisplayName
	}
	if len(input.DisplayName) > 512 {
		return input, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "target display name exceeds 512 bytes")
	}
	if err := dbsafe.Text(input.DisplayName); err != nil {
		return input, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	if body.ProviderMetadata != nil {
		var err error
		input.ProviderMetadata, err = channelconnector.NormalizeOpaqueObject(body.ProviderMetadata)
		if err != nil {
			return input, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "target metadata: "+err.Error())
		}
	}
	return input, nil
}

func channelAddressText(name, value string, maxBytes int) error {
	if strings.TrimSpace(value) == "" || len(value) > maxBytes {
		return apierror.FromCode(openapi.ErrorCodeInvalidRequest, name+" is empty or too large")
	}
	if err := dbsafe.Text(value); err != nil {
		return apierror.FromCode(openapi.ErrorCodeInvalidRequest, name+": "+err.Error())
	}
	return nil
}

// Provider objects use exact wire names. encoding/json alone accepts case aliases
// and null optional scalars even with DisallowUnknownFields, obscuring mistakes.
func decodeChannelRegistrationTarget(raw json.RawMessage) (openapi.ChannelRegistrationTarget, error) {
	var target openapi.ChannelRegistrationTarget
	object, err := jsoncanonical.ParseObject(raw, int(channelconnector.MaxOperationResponseBytes))
	if err != nil {
		return target, err
	}
	if err := validateChannelRegistrationFields(object, true); err != nil {
		return target, err
	}
	err = json.Unmarshal(raw, &target)
	return target, err
}

func validateChannelRegistrationFields(object map[string]any, target bool) error {
	invalid := errors.New("invalid channel registration address")
	for key, value := range object {
		switch key {
		case "definition_id", "provider_ref", "provider_ref_kind", "display_name":
			if _, ok := value.(string); !ok {
				return invalid
			}
		case "provider_metadata":
			if _, ok := value.(map[string]any); !ok {
				return invalid
			}
		case "parent_channel_id":
			if _, ok := value.(string); !target || !ok {
				return invalid
			}
		case "parent":
			parent, ok := value.(map[string]any)
			if !target || !ok {
				return invalid
			}
			if err := validateChannelRegistrationFields(parent, false); err != nil {
				return err
			}
		default:
			return invalid
		}
	}
	return nil
}
