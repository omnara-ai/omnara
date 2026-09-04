package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/oapi-codegen/nullable"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	openapigen "github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/log/logent"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/resourcename"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/ssrf"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/patch"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
)

type createModelProviderConfigCommand struct {
	Name               string
	Preset             string
	APIFormat          modelprotocol.APIFormat
	APIVariant         modelprotocol.APIVariant
	BaseURL            string
	EndpointPath       string
	RequestTimeoutMS   int
	AuthKind           string
	AuthOptions        json.RawMessage
	CredentialSecretID string
}

func createModelProviderConfigCommandFromOpenAPI(
	body openapigen.CreateModelProviderConfigRequest,
) createModelProviderConfigCommand {
	command := createModelProviderConfigCommand{
		Name:               body.Name,
		CredentialSecretID: body.CredentialSecretId,
	}
	if body.Preset != nil {
		command.Preset = string(*body.Preset)
	}
	if body.ApiFormat != nil {
		command.APIFormat = modelprotocol.APIFormat(*body.ApiFormat)
	}
	if body.ApiVariant != nil {
		command.APIVariant = modelprotocol.APIVariant(*body.ApiVariant)
	}
	if body.BaseUrl != nil {
		command.BaseURL = *body.BaseUrl
	}
	if body.EndpointPath != nil {
		command.EndpointPath = *body.EndpointPath
	}
	if body.RequestTimeoutMs != nil {
		command.RequestTimeoutMS = *body.RequestTimeoutMs
	}
	if body.AuthKind != nil {
		command.AuthKind = string(*body.AuthKind)
	}
	command.AuthOptions = body.AuthOptions
	return command
}

func patchModelProviderConfigInputFromOpenAPI(
	orgID, configID storage.ID,
	body openapigen.UpdateModelProviderConfigRequest,
) (modelstore.PatchModelProviderConfigInput, error) {
	patch := modelstore.PatchModelProviderConfigInput{
		OrgID: orgID,
		ID:    configID,
	}
	if body.BaseUrl != nil {
		baseURL := *body.BaseUrl
		patch.BaseURL = &baseURL
	}
	if body.EndpointPath != nil {
		endpointPath := *body.EndpointPath
		patch.EndpointPath = &endpointPath
	}
	if body.RequestTimeoutMs != nil {
		requestTimeoutMS := *body.RequestTimeoutMs
		patch.RequestTimeoutMS = &requestTimeoutMS
	}
	if body.AuthKind != nil {
		authKind := string(*body.AuthKind)
		patch.AuthKind = &authKind
	}
	if body.AuthOptions != nil {
		value := append(json.RawMessage(nil), body.AuthOptions...)
		patch.AuthOptions = &value
	}
	if body.CredentialSecretId != nil {
		parsed, err := parsePublicID(publicid.KindSecret, *body.CredentialSecretId)
		if err != nil {
			return modelstore.PatchModelProviderConfigInput{}, errors.New("invalid credential_secret_id")
		}
		credentialSecretID := parsed
		patch.CredentialSecretID = &credentialSecretID
	}
	return patch, nil
}

func createConfiguredModelInputFromOpenAPI(
	orgID, configID storage.ID,
	body openapigen.CreateConfiguredModelRequest,
) (modelstore.CreateConfiguredModelInput, error) {
	maxOutputTokens, defaultMaxOutputTokens, err := modelstore.ResolveConfiguredModelOutputLimits(
		body.ContextWindowTokens,
		body.MaxOutputTokens,
		body.DefaultMaxOutputTokens,
	)
	if err != nil {
		return modelstore.CreateConfiguredModelInput{}, err
	}
	input := modelstore.CreateConfiguredModelInput{
		OrgID:                  orgID,
		ModelProviderConfigID:  configID,
		Name:                   body.Name,
		ProviderModelSlug:      body.ProviderModelSlug,
		ContextWindowTokens:    body.ContextWindowTokens,
		MaxOutputTokens:        maxOutputTokens,
		DefaultMaxOutputTokens: defaultMaxOutputTokens,
		SupportsTools:          body.SupportsTools,
		APIVariantOptions:      append(json.RawMessage(nil), body.ApiVariantOptions...),
	}
	if body.DefaultCacheRetention != nil {
		input.DefaultCacheRetention = string(*body.DefaultCacheRetention)
	}
	if body.SupportsReasoning != nil {
		input.SupportsReasoning = *body.SupportsReasoning
	}
	if body.DefaultReasoningEffort != nil {
		input.DefaultReasoningEffort = *body.DefaultReasoningEffort
	}
	if body.SupportedReasoningEfforts != nil {
		input.SupportedReasoningEfforts = append([]string(nil), (*body.SupportedReasoningEfforts)...)
	}
	if body.InputModalities != nil {
		input.InputModalities = append([]string(nil), (*body.InputModalities)...)
	}
	if body.OutputModalities != nil {
		input.OutputModalities = append([]string(nil), (*body.OutputModalities)...)
	}
	return input, nil
}

func validateCreateModelProviderConfigRequest(body openapigen.CreateModelProviderConfigRequest) error {
	if body.Preset == nil && body.ApiFormat == nil {
		return errors.New("api_format is required unless preset is provided")
	}
	if body.Preset == nil && body.BaseUrl == nil {
		return errors.New("base_url is required unless preset is provided")
	}
	if body.BaseUrl != nil && strings.TrimSpace(*body.BaseUrl) == "" {
		return errors.New("base_url is required")
	}
	if body.EndpointPath != nil && strings.TrimSpace(*body.EndpointPath) == "" {
		return errors.New("endpoint_path is required")
	}
	if body.AuthKind != nil && strings.TrimSpace(string(*body.AuthKind)) == "" {
		return errors.New("auth_kind is required")
	}
	if strings.TrimSpace(body.CredentialSecretId) == "" {
		return errors.New("credential_secret_id is required")
	}
	return nil
}

func validateUpdateModelProviderConfigRequest(body openapigen.UpdateModelProviderConfigRequest) error {
	if body.BaseUrl != nil && strings.TrimSpace(*body.BaseUrl) == "" {
		return errors.New("base_url is required")
	}
	if body.EndpointPath != nil && strings.TrimSpace(*body.EndpointPath) == "" {
		return errors.New("endpoint_path is required")
	}
	if body.AuthKind != nil && strings.TrimSpace(string(*body.AuthKind)) == "" {
		return errors.New("auth_kind is required")
	}
	if body.CredentialSecretId != nil && strings.TrimSpace(*body.CredentialSecretId) == "" {
		return errors.New("credential_secret_id is required")
	}
	return nil
}

func createModelProviderConfigHasField(body openapigen.CreateModelProviderConfigRequest, field string) bool {
	switch field {
	case "api_format":
		return body.ApiFormat != nil
	case "api_variant":
		return body.ApiVariant != nil
	case "base_url":
		return body.BaseUrl != nil
	case "endpoint_path":
		return body.EndpointPath != nil
	case "request_timeout_ms":
		return body.RequestTimeoutMs != nil
	case "auth_kind":
		return body.AuthKind != nil
	case "auth_options":
		return body.AuthOptions != nil
	default:
		return false
	}
}

func validateModelProviderBaseURLNetworkPolicy(baseURL string, allowInsecureDev bool) error {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if err != nil {
		return errors.New("base_url is invalid")
	}
	if parsed.Host == "" {
		return nil
	}
	hostname := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if hostname == "localhost" || strings.HasSuffix(hostname, ".localhost") {
		if allowInsecureDev {
			return nil
		}
		return errors.New("base_url cannot target localhost unless insecure dev mode is enabled")
	}
	ip := net.ParseIP(hostname)
	if ip == nil {
		return nil
	}
	if ssrf.IsAllowedIP(ip, allowInsecureDev) {
		return nil
	}
	if ip.IsLoopback() {
		return errors.New("base_url cannot target loopback IPs unless insecure dev mode is enabled")
	}
	return errors.New("base_url cannot target private or special-use IP addresses")
}

// presetConflictFields are the body fields a preset materializes server-side and
// therefore must not be combined with an explicit preset.
var presetConflictFields = []string{
	"api_format",
	"api_variant",
	"base_url",
	"endpoint_path",
	"auth_kind",
	"auth_options",
}

func (s strictOpenAPIServer) CreateModelProviderConfig(
	ctx context.Context,
	request openapigen.CreateModelProviderConfigRequestObject,
) (openapigen.CreateModelProviderConfigResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.authorizeModelProviderCredentialBinding(ctx, org); err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapigen.ErrorCodeInvalidRequest, "request body is required")
	}
	body := *request.Body
	canonicalName, err := resourcename.CanonicalizeRequired("model provider config name", body.Name)
	if err != nil {
		return nil, apierror.FromCode(openapigen.ErrorCodeInvalidRequest, err.Error())
	}
	body.Name = canonicalName
	if err := validateCreateModelProviderConfigRequest(body); err != nil {
		return nil, apierror.FromCode(openapigen.ErrorCodeInvalidRequest, err.Error())
	}
	command := createModelProviderConfigCommandFromOpenAPI(body)
	if command.Preset != "" {
		for _, field := range presetConflictFields {
			if createModelProviderConfigHasField(body, field) {
				return nil, apierror.FromCode(openapigen.ErrorCodeInvalidRequest, "preset cannot be combined with "+field)
			}
		}
	}
	if err := applyModelProviderPreset(
		command.Preset,
		&command.APIFormat,
		&command.APIVariant,
		&command.BaseURL,
		&command.EndpointPath,
		&command.AuthKind,
		&command.AuthOptions,
	); err != nil {
		return nil, apierror.FromCode(openapigen.ErrorCodeInvalidRequest, err.Error())
	}
	if err := validateModelProviderBaseURLNetworkPolicy(
		command.BaseURL,
		s.server.allowInsecureModelProviderEndpoints,
	); err != nil {
		return nil, apierror.FromCode(openapigen.ErrorCodeInvalidRequest, err.Error())
	}
	credentialSecretID, err := parsePublicID(publicid.KindSecret, command.CredentialSecretID)
	if err != nil {
		return nil, apierror.FromCode(openapigen.ErrorCodeInvalidRequest, "invalid credential_secret_id")
	}
	record, err := s.server.store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
		OrgID:              org.ID,
		Name:               command.Name,
		APIFormat:          command.APIFormat,
		APIVariant:         command.APIVariant,
		BaseURL:            command.BaseURL,
		EndpointPath:       command.EndpointPath,
		RequestTimeoutMS:   command.RequestTimeoutMS,
		AuthKind:           command.AuthKind,
		AuthOptions:        command.AuthOptions,
		CredentialSecretID: credentialSecretID,
	})
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	configResponse, err := modelProviderConfigResponse(record)
	if err != nil {
		return nil, err
	}
	response := openapigen.CreateModelProviderConfigResponse{
		Config:       configResponse,
		ModelCatalog: s.providerModelCatalog(ctx, org.ID, record),
	}
	if record.Created {
		return openapigen.CreateModelProviderConfig201JSONResponse(response), nil
	}
	return openapigen.CreateModelProviderConfig200JSONResponse(response), nil
}

func (s strictOpenAPIServer) providerModelCatalog(
	ctx context.Context,
	orgID storage.ID,
	record modelstore.ModelProviderConfigRecord,
) openapigen.ModelCatalog {
	if record.APIVariant == modelprotocol.APIVariantBedrock &&
		record.AuthKind == modelstore.ModelProviderAuthKindSigV4 {
		models := []openapigen.DiscoveredProviderModel{}
		return openapigen.ModelCatalog{
			Status: openapigen.ModelCatalogStatusOk,
			Models: &models,
		}
	}
	failed := func(message string) openapigen.ModelCatalog {
		logent.ModelCatalogProbeFailed(ctx, record.ID, message)
		return openapigen.ModelCatalog{
			Status: openapigen.ModelCatalogStatusFailed,
			Error:  &message,
		}
	}
	credential, err := s.server.store.Secrets().ReadOrgOwnedSecretPayload(ctx, secretstore.ReadOrgOwnedSecretPayloadInput{
		OrgID:          orgID,
		SecretID:       record.CredentialSecretID,
		ManagementKind: record.ManagementKind,
		Kind:           secretstore.SecretKindGeneric,
	})
	if err != nil {
		return failed("could not read the credential secret")
	}
	apiKey := credential.Payload[secrets.KeyValue]
	if apiKey == "" {
		return failed("credential secret has no value")
	}
	models, err := s.server.modelDiscoverer(
		ctx,
		record,
		apiKey,
		s.server.allowInsecureModelProviderEndpoints,
	)
	if err != nil {
		return failed(err.Error())
	}
	discovered := make([]openapigen.DiscoveredProviderModel, 0, len(models))
	for _, model := range models {
		entry := openapigen.DiscoveredProviderModel{Slug: model.Slug}
		if model.DisplayName != "" {
			displayName := model.DisplayName
			entry.DisplayName = &displayName
		}
		entry.ContextWindowTokens = model.ContextWindowTokens
		entry.MaxOutputTokens = model.MaxOutputTokens
		discovered = append(discovered, entry)
	}
	return openapigen.ModelCatalog{
		Status: openapigen.ModelCatalogStatusOk,
		Models: &discovered,
	}
}

func (s strictOpenAPIServer) ListModelProviderConfigs(
	ctx context.Context,
	request openapigen.ListModelProviderConfigsRequestObject,
) (openapigen.ListModelProviderConfigsResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	limit, err := parseOpenAPIPageLimit(request.Params.Limit)
	if err != nil {
		return nil, apierror.FromCode(openapigen.ErrorCodeInvalidRequest, err.Error())
	}
	list, err := parseResourceListQuery(resourceListQueryInput{
		Name: request.Params.Name, Sort: optionalString(request.Params.Sort),
		Cursor: request.Params.Cursor, ListKind: "model_provider_configs",
		Scope: org.ID.String(), IDKind: publicid.KindModelProviderConfig,
		AllowedSorts: defaultResourceSorts,
	})
	if err != nil {
		return nil, apierror.FromCode(openapigen.ErrorCodeInvalidRequest, err.Error())
	}
	page, err := s.server.store.Models().ListModelProviderConfigs(
		ctx,
		modelstore.ListModelProviderConfigsInput{OrgID: org.ID, Limit: limit, List: list},
	)
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	out := make([]openapigen.ModelProviderConfig, 0, len(page.Configs))
	for _, record := range page.Configs {
		response, err := modelProviderConfigResponse(record)
		if err != nil {
			return nil, err
		}
		out = append(out, response)
	}
	nextCursor, err := encodeResourceListNextCursor(
		page.HasMore, page.Next, list, "model_provider_configs",
		org.ID.String(), publicid.KindModelProviderConfig, nil,
	)
	if err != nil {
		return nil, err
	}
	return openapigen.ListModelProviderConfigs200JSONResponse(
		openapigen.ModelProviderConfigList{Data: out, NextCursor: nullableFromPtr(nextCursor)},
	), nil
}

func (s strictOpenAPIServer) GetModelProviderConfig(
	ctx context.Context,
	request openapigen.GetModelProviderConfigRequestObject,
) (openapigen.GetModelProviderConfigResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	configID, ok := parseOpenAPIPublicID(publicid.KindModelProviderConfig, request.ModelProviderConfigID)
	if !ok {
		return nil, apierror.FromCode(openapigen.ErrorCodeNotFound, "not found")
	}
	record, err := s.server.store.Models().GetModelProviderConfig(ctx, org.ID, configID)
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	response, err := modelProviderConfigResponse(record)
	if err != nil {
		return nil, err
	}
	return openapigen.GetModelProviderConfig200JSONResponse(response), nil
}

func (s strictOpenAPIServer) GetModelCatalog(
	ctx context.Context,
	request openapigen.GetModelCatalogRequestObject,
) (openapigen.GetModelCatalogResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	configID, ok := parseOpenAPIPublicID(publicid.KindModelProviderConfig, request.ModelProviderConfigID)
	if !ok {
		return nil, apierror.FromCode(openapigen.ErrorCodeNotFound, "not found")
	}
	record, err := s.server.store.Models().GetModelProviderConfig(ctx, org.ID, configID)
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	return openapigen.GetModelCatalog200JSONResponse(s.providerModelCatalog(ctx, org.ID, record)), nil
}

func (s strictOpenAPIServer) UpdateModelProviderConfig(
	ctx context.Context,
	request openapigen.UpdateModelProviderConfigRequestObject,
) (openapigen.UpdateModelProviderConfigResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.authorizeModelProviderCredentialBinding(ctx, org); err != nil {
		return nil, err
	}
	configID, ok := parseOpenAPIPublicID(publicid.KindModelProviderConfig, request.ModelProviderConfigID)
	if !ok {
		return nil, apierror.FromCode(openapigen.ErrorCodeNotFound, "not found")
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapigen.ErrorCodeInvalidRequest, "request body is required")
	}
	body := *request.Body
	if err := validateUpdateModelProviderConfigRequest(body); err != nil {
		return nil, apierror.FromCode(openapigen.ErrorCodeInvalidRequest, err.Error())
	}
	input, err := patchModelProviderConfigInputFromOpenAPI(org.ID, configID, body)
	if err != nil {
		return nil, apierror.FromCode(openapigen.ErrorCodeInvalidRequest, err.Error())
	}
	if input.BaseURL != nil {
		if err := validateModelProviderBaseURLNetworkPolicy(
			*input.BaseURL,
			s.server.allowInsecureModelProviderEndpoints,
		); err != nil {
			return nil, apierror.FromCode(openapigen.ErrorCodeInvalidRequest, err.Error())
		}
	}
	record, err := s.server.store.Models().PatchModelProviderConfig(ctx, input)
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	response, err := modelProviderConfigResponse(record)
	if err != nil {
		return nil, err
	}
	return openapigen.UpdateModelProviderConfig200JSONResponse(response), nil
}

func (s strictOpenAPIServer) DeleteModelProviderConfig(
	ctx context.Context,
	request openapigen.DeleteModelProviderConfigRequestObject,
) (openapigen.DeleteModelProviderConfigResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	configID, ok := parseOpenAPIPublicID(publicid.KindModelProviderConfig, request.ModelProviderConfigID)
	if !ok {
		return nil, apierror.FromCode(openapigen.ErrorCodeNotFound, "not found")
	}
	_, err = s.server.store.Models().DeleteModelProviderConfig(ctx, org.ID, configID)
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	return openapigen.DeleteModelProviderConfig204Response{}, nil
}

func (s strictOpenAPIServer) CreateConfiguredModel(
	ctx context.Context,
	request openapigen.CreateConfiguredModelRequestObject,
) (openapigen.CreateConfiguredModelResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	configID, ok := parseOpenAPIPublicID(publicid.KindModelProviderConfig, request.ModelProviderConfigID)
	if !ok {
		return nil, apierror.FromCode(openapigen.ErrorCodeNotFound, "not found")
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapigen.ErrorCodeInvalidRequest, "request body is required")
	}
	input, err := createConfiguredModelInputFromOpenAPI(org.ID, configID, *request.Body)
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	record, err := s.server.store.Models().CreateConfiguredModel(ctx, input)
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	response, err := configuredModelResponse(record)
	if err != nil {
		return nil, err
	}
	if record.Created {
		return openapigen.CreateConfiguredModel201JSONResponse(response), nil
	}
	return openapigen.CreateConfiguredModel200JSONResponse(response), nil
}

func (s strictOpenAPIServer) ListConfiguredModels(
	ctx context.Context,
	request openapigen.ListConfiguredModelsRequestObject,
) (openapigen.ListConfiguredModelsResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	configID, ok := parseOpenAPIPublicID(publicid.KindModelProviderConfig, request.ModelProviderConfigID)
	if !ok {
		return nil, apierror.FromCode(openapigen.ErrorCodeNotFound, "not found")
	}
	limit, after, err := parseOpenAPIPageParams(
		request.Params.Limit,
		request.Params.Cursor,
		publicid.KindConfiguredModel,
	)
	if err != nil {
		return nil, apierror.FromCode(openapigen.ErrorCodeInvalidRequest, err.Error())
	}
	page, err := s.server.store.Models().ListConfiguredModels(
		ctx,
		modelstore.ListConfiguredModelsInput{
			OrgID: org.ID, ProviderConfigID: configID, Limit: limit,
			After: listing.KeysetCursor{
				Set: after.Set, CreatedAt: after.CreatedAt, ID: after.ID,
			},
		},
	)
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	out := make([]openapigen.ConfiguredModel, 0, len(page.Models))
	var last modelstore.ConfiguredModelRecord
	for _, record := range page.Models {
		response, err := configuredModelResponse(record)
		if err != nil {
			return nil, err
		}
		out = append(out, response)
		last = record
	}
	nextCursor, err := encodeNextCursor(page.HasMore, last.CreatedAt, publicid.KindConfiguredModel, last.ID)
	if err != nil {
		return nil, err
	}
	return openapigen.ListConfiguredModels200JSONResponse(
		openapigen.ConfiguredModelList{Data: out, NextCursor: nullableFromPtr(nextCursor)},
	), nil
}

func (s strictOpenAPIServer) UpdateConfiguredModel(
	ctx context.Context,
	request openapigen.UpdateConfiguredModelRequestObject,
) (openapigen.UpdateConfiguredModelResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	configID, ok := parseOpenAPIPublicID(publicid.KindModelProviderConfig, request.ModelProviderConfigID)
	if !ok {
		return nil, apierror.FromCode(openapigen.ErrorCodeNotFound, "not found")
	}
	configuredModelID, ok := parseOpenAPIPublicID(publicid.KindConfiguredModel, request.ConfiguredModelID)
	if !ok {
		return nil, apierror.FromCode(openapigen.ErrorCodeNotFound, "not found")
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapigen.ErrorCodeInvalidRequest, "request body is required")
	}
	input, err := patchConfiguredModelInput(
		org.ID,
		configID,
		configuredModelID,
		*request.Body,
	)
	if err != nil {
		return nil, apierror.FromCode(openapigen.ErrorCodeInvalidRequest, err.Error())
	}
	record, err := s.server.store.Models().PatchConfiguredModel(ctx, input)
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	response, err := configuredModelResponse(record)
	if err != nil {
		return nil, err
	}
	return openapigen.UpdateConfiguredModel200JSONResponse(response), nil
}

func patchConfiguredModelInput(
	orgID, configID, configuredModelID storage.ID,
	body openapigen.UpdateConfiguredModelRequest,
) (modelstore.PatchConfiguredModelInput, error) {
	input := modelstore.PatchConfiguredModelInput{
		OrgID:                 orgID,
		ModelProviderConfigID: configID,
		ID:                    configuredModelID,
	}
	if body.Name != nil {
		canonicalName, err := resourcename.CanonicalizeRequired("configured model name", *body.Name)
		if err != nil {
			return modelstore.PatchConfiguredModelInput{}, err
		}
		input.Name = &canonicalName
	}
	if body.ProviderModelSlug != nil {
		input.ProviderModelSlug = body.ProviderModelSlug
	}
	if body.ContextWindowTokens != nil {
		if *body.ContextWindowTokens < 1 {
			return modelstore.PatchConfiguredModelInput{}, errors.New("context_window_tokens must be at least 1")
		}
		input.ContextWindowTokens = body.ContextWindowTokens
	}
	if body.MaxOutputTokens != nil {
		if *body.MaxOutputTokens < 1 {
			return modelstore.PatchConfiguredModelInput{}, errors.New("max_output_tokens must be at least 1")
		}
		input.MaxOutputTokens = body.MaxOutputTokens
	}
	if body.DefaultMaxOutputTokens.IsSpecified() {
		if err := applyNullableIntPatch(
			"default_max_output_tokens",
			body.DefaultMaxOutputTokens,
			1,
			&input.DefaultMaxOutputTokens,
		); err != nil {
			return modelstore.PatchConfiguredModelInput{}, err
		}
	}
	if body.DefaultCacheRetention != nil {
		value := string(*body.DefaultCacheRetention)
		input.DefaultCacheRetention = &value
	}
	if body.SupportsTools != nil {
		input.SupportsTools = body.SupportsTools
	}
	if body.SupportsReasoning != nil {
		input.SupportsReasoning = body.SupportsReasoning
	}
	if body.DefaultReasoningEffort != nil {
		input.DefaultReasoningEffort = body.DefaultReasoningEffort
	}
	if body.SupportedReasoningEfforts != nil {
		values := append([]string(nil), (*body.SupportedReasoningEfforts)...)
		input.SupportedReasoningEfforts = &values
	}
	if body.InputModalities != nil {
		values := append([]string(nil), (*body.InputModalities)...)
		input.InputModalities = &values
	}
	if body.OutputModalities != nil {
		values := append([]string(nil), (*body.OutputModalities)...)
		input.OutputModalities = &values
	}
	if body.ApiVariantOptions != nil {
		value := append(json.RawMessage(nil), body.ApiVariantOptions...)
		input.APIVariantOptions = &value
	}
	return input, nil
}

func applyNullableIntPatch(
	name string,
	value nullable.Nullable[int],
	minValue int,
	target *patch.NullableInt,
) error {
	target.Set = true
	if value.IsNull() {
		return nil
	}
	n, err := value.Get()
	if err != nil {
		return err
	}
	if n < minValue {
		return fmt.Errorf("%s must be at least %d", name, minValue)
	}
	target.Value = &n
	return nil
}

func (s strictOpenAPIServer) DeleteConfiguredModel(
	ctx context.Context,
	request openapigen.DeleteConfiguredModelRequestObject,
) (openapigen.DeleteConfiguredModelResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	configID, ok := parseOpenAPIPublicID(publicid.KindModelProviderConfig, request.ModelProviderConfigID)
	if !ok {
		return nil, apierror.FromCode(openapigen.ErrorCodeNotFound, "not found")
	}
	configuredModelID, ok := parseOpenAPIPublicID(publicid.KindConfiguredModel, request.ConfiguredModelID)
	if !ok {
		return nil, apierror.FromCode(openapigen.ErrorCodeNotFound, "not found")
	}
	modelRecord, err := s.server.store.Models().GetConfiguredModelDisplay(ctx, org.ID, configuredModelID)
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	if modelRecord.ModelProviderConfigID != configID {
		return nil, apierror.FromCode(openapigen.ErrorCodeNotFound, "not found")
	}
	_, err = s.server.store.Models().DeleteConfiguredModel(ctx, org.ID, configuredModelID)
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	return openapigen.DeleteConfiguredModel204Response{}, nil
}

func (s strictOpenAPIServer) CreateProjectModelGrant(
	ctx context.Context,
	request openapigen.CreateProjectModelGrantRequestObject,
) (openapigen.CreateProjectModelGrantResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapigen.ErrorCodeInvalidRequest, "request body is required")
	}
	principal, ok := principalFromContext(ctx)
	if !ok || principal.Type != identitystore.PrincipalTypeUser || principal.ID == storage.NilID {
		return nil, apierror.FromCode(openapigen.ErrorCodeForbidden, "authenticated user principal is required")
	}
	body := *request.Body
	configuredModelID, err := parsePublicID(publicid.KindConfiguredModel, body.ConfiguredModelId)
	if err != nil {
		return nil, apierror.FromCode(openapigen.ErrorCodeInvalidRequest, "invalid configured_model_id")
	}
	input := modelstore.CreateProjectModelGrantInput{
		OrgID:                     scope.org.ID,
		ProjectID:                 scope.project.ID,
		ConfiguredModelID:         configuredModelID,
		ContextWindowTokens:       body.ContextWindowTokens,
		MaxOutputTokens:           body.MaxOutputTokens,
		DefaultMaxOutputTokens:    body.DefaultMaxOutputTokens,
		SupportsTools:             body.SupportsTools,
		SupportsReasoning:         body.SupportsReasoning,
		SupportedReasoningEfforts: nonNilStringSliceFromPtr(body.SupportedReasoningEfforts),
		InputModalities:           nonNilStringSliceFromPtr(body.InputModalities),
		OutputModalities:          nonNilStringSliceFromPtr(body.OutputModalities),
	}
	if body.DefaultCacheRetention != nil {
		input.DefaultCacheRetention = string(*body.DefaultCacheRetention)
	}
	if body.DefaultReasoningEffort != nil {
		input.DefaultReasoningEffort = *body.DefaultReasoningEffort
	}
	record, err := s.server.store.Models().CreateProjectModelGrant(ctx, input)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	response, err := projectModelGrantResponse(record)
	if err != nil {
		return nil, err
	}
	if record.Created {
		return openapigen.CreateProjectModelGrant201JSONResponse(
			openapigen.ProjectModelGrantEnvelope{Grant: response},
		), nil
	}
	return openapigen.CreateProjectModelGrant200JSONResponse(openapigen.ProjectModelGrantEnvelope{Grant: response}), nil
}

func (s strictOpenAPIServer) ListProjectModelGrants(
	ctx context.Context,
	request openapigen.ListProjectModelGrantsRequestObject,
) (openapigen.ListProjectModelGrantsResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	limit, err := parseOpenAPIPageLimit(request.Params.Limit)
	if err != nil {
		return nil, apierror.FromCode(openapigen.ErrorCodeInvalidRequest, err.Error())
	}
	scopeKey := scope.org.ID.String() + "/" + scope.project.ID.String()
	list, err := parseResourceListQuery(resourceListQueryInput{
		Name: request.Params.Name, Sort: optionalString(request.Params.Sort),
		Cursor: request.Params.Cursor, ListKind: "project_model_grants",
		Scope: scopeKey, IDKind: publicid.KindProjectModelGrant,
		AllowedSorts: defaultResourceSorts,
	})
	if err != nil {
		return nil, apierror.FromCode(openapigen.ErrorCodeInvalidRequest, err.Error())
	}
	page, err := s.server.store.Models().ListProjectModelGrants(
		ctx,
		modelstore.ListProjectModelGrantsInput{
			OrgID:     scope.org.ID,
			ProjectID: scope.project.ID,
			Limit:     limit,
			List:      list,
		},
	)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	out := make([]openapigen.ProjectModelGrantListItem, 0, len(page.Grants))
	for _, record := range page.Grants {
		response, err := projectModelGrantResponse(record.Grant)
		if err != nil {
			return nil, err
		}
		model, err := configuredModelSummaryResponse(record.Model)
		if err != nil {
			return nil, err
		}
		out = append(out, openapigen.ProjectModelGrantListItem{Grant: response, Model: model})
	}
	nextCursor, err := encodeResourceListNextCursor(
		page.HasMore, page.Next, list, "project_model_grants",
		scopeKey, publicid.KindProjectModelGrant, nil,
	)
	if err != nil {
		return nil, err
	}
	return openapigen.ListProjectModelGrants200JSONResponse(
		openapigen.ListProjectModelGrantsResponse{Data: out, NextCursor: nullableFromPtr(nextCursor)},
	), nil
}

func configuredModelSummaryResponse(
	record modelstore.ConfiguredModelSummaryRecord,
) (openapigen.ConfiguredModelSummary, error) {
	id, err := publicID(publicid.KindConfiguredModel, record.ID)
	if err != nil {
		return openapigen.ConfiguredModelSummary{}, err
	}
	orgID, err := publicID(publicid.KindOrganization, record.OrgID)
	if err != nil {
		return openapigen.ConfiguredModelSummary{}, err
	}
	providerConfigID, err := publicID(publicid.KindModelProviderConfig, record.ModelProviderConfigID)
	if err != nil {
		return openapigen.ConfiguredModelSummary{}, err
	}
	return openapigen.ConfiguredModelSummary{
		Id:                    id,
		OrgId:                 orgID,
		ModelProviderConfigId: providerConfigID,
		Name:                  record.Name,
		ProviderConfig:        record.ProviderConfigName,
		CreatedAt:             record.CreatedAt,
		UpdatedAt:             record.UpdatedAt,
	}, nil
}

func (s strictOpenAPIServer) UpdateProjectModelGrant(
	ctx context.Context,
	request openapigen.UpdateProjectModelGrantRequestObject,
) (openapigen.UpdateProjectModelGrantResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapigen.ErrorCodeInvalidRequest, "request body is required")
	}
	grantID, ok := parseOpenAPIPublicID(publicid.KindProjectModelGrant, request.ModelGrantID)
	if !ok {
		return nil, apierror.FromCode(openapigen.ErrorCodeNotFound, "not found")
	}
	input, err := updateProjectModelGrantInput(
		scope.org.ID,
		scope.project.ID,
		grantID,
		*request.Body,
	)
	if err != nil {
		return nil, apierror.FromCode(openapigen.ErrorCodeInvalidRequest, err.Error())
	}
	record, err := s.server.store.Models().UpdateProjectModelGrant(ctx, input)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	response, err := projectModelGrantResponse(record)
	if err != nil {
		return nil, err
	}
	return openapigen.UpdateProjectModelGrant200JSONResponse(openapigen.ProjectModelGrantEnvelope{Grant: response}), nil
}

func updateProjectModelGrantInput(
	orgID, projectID, grantID storage.ID,
	body openapigen.UpdateProjectModelGrantRequest,
) (modelstore.UpdateProjectModelGrantInput, error) {
	input := modelstore.UpdateProjectModelGrantInput{
		OrgID:     orgID,
		ProjectID: projectID,
		ID:        grantID,
	}
	nullableIntFields := []struct {
		name   string
		value  nullable.Nullable[int]
		target *patch.NullableInt
		min    int
	}{
		{name: "context_window_tokens", value: body.ContextWindowTokens, target: &input.ContextWindowTokens, min: 1},
		{name: "max_output_tokens", value: body.MaxOutputTokens, target: &input.MaxOutputTokens, min: 1},
		{
			name:   "default_max_output_tokens",
			value:  body.DefaultMaxOutputTokens,
			target: &input.DefaultMaxOutputTokens,
			min:    1,
		},
	}
	for _, field := range nullableIntFields {
		if !field.value.IsSpecified() {
			continue
		}
		if err := applyNullableIntPatch(field.name, field.value, field.min, field.target); err != nil {
			return modelstore.UpdateProjectModelGrantInput{}, err
		}
	}
	input.DefaultCacheRetention = stringPatchFromNullable(body.DefaultCacheRetention)
	input.SupportsTools = nullableBoolPatchFromBool(body.SupportsTools)
	input.SupportsReasoning = nullableBoolPatchFromBool(body.SupportsReasoning)
	input.DefaultReasoningEffort = stringPatchFromNullable(body.DefaultReasoningEffort)
	if body.SupportedReasoningEfforts != nil {
		values := append([]string(nil), (*body.SupportedReasoningEfforts)...)
		input.SupportedReasoningEfforts = &values
	}
	if body.InputModalities != nil {
		values := append([]string(nil), (*body.InputModalities)...)
		input.InputModalities = &values
	}
	if body.OutputModalities != nil {
		values := append([]string(nil), (*body.OutputModalities)...)
		input.OutputModalities = &values
	}
	return input, nil
}

func (s strictOpenAPIServer) DeleteProjectModelGrant(
	ctx context.Context,
	request openapigen.DeleteProjectModelGrantRequestObject,
) (openapigen.DeleteProjectModelGrantResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	grantID, ok := parseOpenAPIPublicID(publicid.KindProjectModelGrant, request.ModelGrantID)
	if !ok {
		return nil, apierror.FromCode(openapigen.ErrorCodeNotFound, "not found")
	}
	if _, err := s.server.store.Models().DeleteProjectModelGrant(
		ctx,
		scope.org.ID,
		scope.project.ID,
		grantID,
	); err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	return openapigen.DeleteProjectModelGrant204Response{}, nil
}

func modelProviderConfigResponse(record modelstore.ModelProviderConfigRecord) (openapigen.ModelProviderConfig, error) {
	id, err := publicID(publicid.KindModelProviderConfig, record.ID)
	if err != nil {
		return openapigen.ModelProviderConfig{}, err
	}
	orgID, err := publicID(publicid.KindOrganization, record.OrgID)
	if err != nil {
		return openapigen.ModelProviderConfig{}, err
	}
	credentialSecretID, err := publicID(publicid.KindSecret, record.CredentialSecretID)
	if err != nil {
		return openapigen.ModelProviderConfig{}, err
	}
	return openapigen.ModelProviderConfig{
		Id:                 id,
		OrgId:              orgID,
		ManagementKind:     openapigen.ManagementKind(record.ManagementKind),
		Name:               record.Name,
		ApiFormat:          openapigen.ModelAPIFormat(record.APIFormat),
		ApiVariant:         string(record.APIVariant),
		BaseUrl:            record.BaseURL,
		EndpointPath:       record.EndpointPath,
		RequestTimeoutMs:   record.RequestTimeoutMS,
		AuthKind:           record.AuthKind,
		AuthOptions:        jsonOrFallback(record.AuthOptions, json.RawMessage(`{}`)),
		CredentialSecretId: credentialSecretID,
		CreatedAt:          record.CreatedAt,
		UpdatedAt:          record.UpdatedAt,
	}, nil
}

func configuredModelResponse(record modelstore.ConfiguredModelRecord) (openapigen.ConfiguredModel, error) {
	id, err := publicID(publicid.KindConfiguredModel, record.ID)
	if err != nil {
		return openapigen.ConfiguredModel{}, err
	}
	orgID, err := publicID(publicid.KindOrganization, record.OrgID)
	if err != nil {
		return openapigen.ConfiguredModel{}, err
	}
	configID, err := publicID(publicid.KindModelProviderConfig, record.ModelProviderConfigID)
	if err != nil {
		return openapigen.ConfiguredModel{}, err
	}
	revisionID, err := publicID(publicid.KindConfiguredModelRevision, record.CurrentRevisionID)
	if err != nil {
		return openapigen.ConfiguredModel{}, err
	}
	supportedReasoningEfforts := cloneStringSlice(record.SupportedReasoningEfforts)
	inputModalities := cloneStringSlice(record.InputModalities)
	outputModalities := cloneStringSlice(record.OutputModalities)
	var cacheRetention *openapigen.ModelCacheRetention
	if record.DefaultCacheRetention != "" {
		value := openapigen.ModelCacheRetention(record.DefaultCacheRetention)
		cacheRetention = &value
	}
	return openapigen.ConfiguredModel{
		Id:                        id,
		OrgId:                     orgID,
		ModelProviderConfigId:     configID,
		ManagementKind:            openapigen.ManagementKind(record.ManagementKind),
		Name:                      record.Name,
		CurrentRevisionId:         revisionID,
		ProviderModelSlug:         record.ProviderModelSlug,
		ContextWindowTokens:       record.ContextWindowTokens,
		MaxOutputTokens:           record.MaxOutputTokens,
		DefaultMaxOutputTokens:    nullableFromPtr(record.DefaultMaxOutputTokens),
		DefaultCacheRetention:     cacheRetention,
		SupportsTools:             record.SupportsTools,
		SupportsReasoning:         record.SupportsReasoning,
		DefaultReasoningEffort:    record.DefaultReasoningEffort,
		SupportedReasoningEfforts: supportedReasoningEfforts,
		InputModalities:           inputModalities,
		OutputModalities:          outputModalities,
		ApiVariantOptions:         jsonOrFallback(record.APIVariantOptions, json.RawMessage(`{}`)),
		CreatedAt:                 record.CreatedAt,
		UpdatedAt:                 record.UpdatedAt,
		RevisionCreatedAt:         record.RevisionCreatedAt,
	}, nil
}

func projectModelGrantResponse(record modelstore.ProjectModelGrantRecord) (openapigen.ProjectModelGrant, error) {
	id, err := publicID(publicid.KindProjectModelGrant, record.ID)
	if err != nil {
		return openapigen.ProjectModelGrant{}, err
	}
	orgID, err := publicID(publicid.KindOrganization, record.OrgID)
	if err != nil {
		return openapigen.ProjectModelGrant{}, err
	}
	projectID, err := publicID(publicid.KindProject, record.ProjectID)
	if err != nil {
		return openapigen.ProjectModelGrant{}, err
	}
	configuredModelID, err := publicID(publicid.KindConfiguredModel, record.ConfiguredModelID)
	if err != nil {
		return openapigen.ProjectModelGrant{}, err
	}
	var cacheRetention *openapigen.ModelCacheRetention
	if record.DefaultCacheRetention != "" {
		value := openapigen.ModelCacheRetention(record.DefaultCacheRetention)
		cacheRetention = &value
	}
	var defaultReasoningEffort *string
	if record.DefaultReasoningEffort != "" {
		defaultReasoningEffort = &record.DefaultReasoningEffort
	}
	return openapigen.ProjectModelGrant{
		Id:                        id,
		OrgId:                     orgID,
		ProjectId:                 projectID,
		ConfiguredModelId:         configuredModelID,
		ContextWindowTokens:       nullableFromPtr(record.ContextWindowTokens),
		MaxOutputTokens:           nullableFromPtr(record.MaxOutputTokens),
		DefaultMaxOutputTokens:    nullableFromPtr(record.DefaultMaxOutputTokens),
		DefaultCacheRetention:     cacheRetention,
		SupportsTools:             nullableFromPtr(record.SupportsTools),
		SupportsReasoning:         nullableFromPtr(record.SupportsReasoning),
		DefaultReasoningEffort:    defaultReasoningEffort,
		SupportedReasoningEfforts: cloneStringSlice(record.SupportedReasoningEfforts),
		InputModalities:           cloneStringSlice(record.InputModalities),
		OutputModalities:          cloneStringSlice(record.OutputModalities),
		CreatedAt:                 record.CreatedAt,
		UpdatedAt:                 record.UpdatedAt,
	}, nil
}

func nonNilStringSliceFromPtr(value *[]string) []string {
	if value == nil {
		return []string{}
	}
	return append([]string(nil), (*value)...)
}

func cloneStringSlice(value []string) []string {
	return append([]string{}, value...)
}

func applyModelProviderPreset(
	preset string,
	apiFormat *modelprotocol.APIFormat,
	apiVariant *modelprotocol.APIVariant,
	baseURL, endpointPath, authKind *string,
	authOptions *json.RawMessage,
) error {
	switch preset {
	case "":
		return nil
	case "openai":
		*apiFormat = modelprotocol.APIFormatOpenAIResponses
		*apiVariant = modelprotocol.APIVariantDefault
		*baseURL = "https://api.openai.com/v1"
		*endpointPath = modelstore.DefaultModelProviderEndpointPath(*apiFormat)
		*authKind = modelstore.DefaultModelProviderAuthKind(*apiFormat)
		*authOptions = modelstore.DefaultModelProviderAuthOptions(*apiFormat, *authKind)
	case "openrouter":
		*apiFormat = modelprotocol.APIFormatOpenAIChatCompletions
		*apiVariant = modelprotocol.APIVariantOpenRouter
		*baseURL = "https://openrouter.ai/api/v1"
		*endpointPath = modelstore.DefaultModelProviderEndpointPath(*apiFormat)
		*authKind = modelstore.DefaultModelProviderAuthKind(*apiFormat)
		*authOptions = modelstore.DefaultModelProviderAuthOptions(*apiFormat, *authKind)
	case "anthropic":
		*apiFormat = modelprotocol.APIFormatAnthropicMessages
		*apiVariant = modelprotocol.APIVariantDefault
		*baseURL = "https://api.anthropic.com/v1"
		*endpointPath = modelstore.DefaultModelProviderEndpointPath(*apiFormat)
		*authKind = modelstore.DefaultModelProviderAuthKind(*apiFormat)
		*authOptions = modelstore.DefaultModelProviderAuthOptions(*apiFormat, *authKind)
	default:
		return errors.New("unsupported model provider preset")
	}
	return nil
}

func (s strictOpenAPIServer) authorizeModelProviderCredentialBinding(
	ctx context.Context,
	org identitystore.OrgRecord,
) error {
	principal, ok := principalFromContext(ctx)
	if !ok || !identitystore.IsAccountPrincipal(principal) {
		return apierror.FromCode(openapigen.ErrorCodeForbidden, "authenticated account principal is required")
	}
	allowed, err := s.server.store.Identity().AuthorizeOrg(ctx, identitystore.AuthorizeOrgInput{
		Principal: principal,
		OrgID:     org.ID,
		Action:    identitystore.OrgActionSecretsManage,
	})
	if err != nil {
		return authorizationAPIError(ctx, err)
	}
	if !allowed {
		return apierror.FromCode(openapigen.ErrorCodeForbidden, "forbidden")
	}
	return nil
}
