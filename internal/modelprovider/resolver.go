package modelprovider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/metrics"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/anthropicmessages"
	"github.com/omnara-ai/omnara/internal/model/openaichatcompletions"
	"github.com/omnara-ai/omnara/internal/model/openairesponses"
	"github.com/omnara-ai/omnara/internal/model/route"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/sigv4"
	"github.com/omnara-ai/omnara/internal/ssrf"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type Resolver struct {
	Models                *modelstore.Store
	Secrets               *secretstore.Store
	HTTPRecorder          *metrics.HTTPClientRecorder
	AllowLoopback         bool
	SigV4CredentialCache  *sigv4.CredentialCache
	OpenRouterAttribution OpenRouterAttribution
}

type OpenRouterAttribution struct {
	SiteURL       string
	AppTitle      string
	AppCategories []string
}

var (
	defaultHTTPTransport  = ssrf.NewTransport(ssrf.TransportOptions{DisableResponseHeaderTimeout: true})
	loopbackHTTPTransport = ssrf.NewTransport(
		ssrf.TransportOptions{AllowLoopback: true, DisableResponseHeaderTimeout: true},
	)
)

func (r Resolver) Resolve(ctx context.Context, selection model.Selection) (model.ResolvedClient, error) {
	if r.Models == nil || r.Secrets == nil {
		return model.ResolvedClient{}, errors.New("model provider resolver model and secret stores are required")
	}
	orgID, err := storage.ParseID(selection.OrgID)
	if err != nil {
		return model.ResolvedClient{}, resolverError(
			model.ErrorKindInvalidRequest,
			"invalid_model_selection",
			"The model selection contains an invalid organization identifier.",
			err,
		)
	}
	projectID, err := storage.ParseID(selection.ProjectID)
	if err != nil {
		return model.ResolvedClient{}, resolverError(
			model.ErrorKindInvalidRequest,
			"invalid_model_selection",
			"The model selection contains an invalid project identifier.",
			err,
		)
	}
	revisionID, err := storage.ParseID(selection.ConfiguredModelRevisionID)
	if err != nil {
		return model.ResolvedClient{}, resolverError(
			model.ErrorKindInvalidRequest,
			"invalid_model_selection",
			"The model selection contains an invalid configured-model revision identifier.",
			err,
		)
	}
	revision, err := r.Models.GetConfiguredModelRevisionForUse(ctx, orgID, revisionID)
	if err != nil {
		if storeerr.IsNotFound(err) {
			return model.ResolvedClient{}, resolverError(
				model.ErrorKindInvalidRequest,
				"configured_model_revision_unavailable",
				"The configured model revision is no longer available.",
				err,
			)
		}
		return model.ResolvedClient{}, err
	}
	providerConfig, err := r.Models.GetModelProviderConfig(ctx, orgID, revision.ModelProviderConfigID)
	if err != nil {
		if storeerr.IsNotFound(err) {
			return model.ResolvedClient{}, resolverError(
				model.ErrorKindInvalidRequest,
				"model_provider_config_unavailable",
				"The configured model provider is no longer available.",
				err,
			)
		}
		return model.ResolvedClient{}, err
	}
	if strings.TrimSpace(providerConfig.EndpointPath) == "" {
		return model.ResolvedClient{}, resolverError(
			model.ErrorKindInvalidRequest,
			"invalid_model_provider_config",
			"The configured model provider has no endpoint path.",
			errors.New("model endpoint path is required"),
		)
	}
	grant, err := r.Models.GetActiveProjectModelGrantForConfiguredModel(
		ctx,
		orgID,
		projectID,
		revision.ConfiguredModelID,
	)
	if err != nil {
		if storeerr.IsNotFound(err) {
			return model.ResolvedClient{}, resolverError(
				model.ErrorKindAuth,
				"model_grant_unavailable",
				"This project does not currently have access to the configured model.",
				fmt.Errorf("configured model has no active project grant: %w", storeerr.ErrModelGrantUnavailable),
			)
		}
		return model.ResolvedClient{}, err
	}
	effectiveRevision, err := modelstore.EffectiveConfiguredModelRevisionForProjectGrant(
		providerConfig.APIFormat,
		revision,
		grant,
	)
	if err != nil {
		return model.ResolvedClient{}, resolverError(
			model.ErrorKindInvalidRequest,
			"invalid_model_policy",
			"The live project model policy is incompatible with the configured model.",
			err,
		)
	}
	effectiveRevision, err = modelstore.EffectiveConfiguredModelRevisionForAgentOptions(
		providerConfig.APIFormat,
		effectiveRevision,
		selection.Overrides,
	)
	if err != nil {
		return model.ResolvedClient{}, resolverError(
			model.ErrorKindInvalidRequest,
			"invalid_agent_model_options",
			"The agent model options are incompatible with the live model policy.",
			err,
		)
	}
	credentialKind, err := modelstore.ModelProviderCredentialSecretKind(providerConfig.AuthKind)
	if err != nil {
		return model.ResolvedClient{}, resolverError(
			model.ErrorKindInvalidRequest,
			"invalid_model_provider_auth",
			"The configured model provider authentication settings are invalid.",
			err,
		)
	}
	credential, err := r.Secrets.ReadOrgOwnedSecretPayload(
		ctx,
		secretstore.ReadOrgOwnedSecretPayloadInput{
			OrgID:          orgID,
			SecretID:       providerConfig.CredentialSecretID,
			ManagementKind: providerConfig.ManagementKind,
			Kind:           credentialKind,
		},
	)
	if err != nil {
		if storeerr.IsNotFound(err) {
			return model.ResolvedClient{}, resolverError(
				model.ErrorKindAuth,
				"model_credential_unavailable",
				"The configured model provider credential is unavailable.",
				err,
			)
		}
		return model.ResolvedClient{}, err
	}
	if credentialKind == secrets.KindGeneric && credential.Payload[secrets.KeyValue] == "" {
		return model.ResolvedClient{}, resolverError(
			model.ErrorKindAuth,
			"model_credential_unavailable",
			"The configured model provider credential is empty.",
			errors.New("model provider credential secret value is required"),
		)
	}
	var auth route.Auth
	if providerConfig.AuthKind == modelstore.ModelProviderAuthKindSigV4 {
		auth, err = r.sigV4RouteAuth(providerConfig, credential)
	} else {
		auth, err = routeAuthForProviderConfig(providerConfig, credential.Payload[secrets.KeyValue])
	}
	if err != nil {
		return model.ResolvedClient{}, resolverError(
			model.ErrorKindInvalidRequest,
			"invalid_model_provider_auth",
			"The configured model provider authentication settings are invalid.",
			err,
		)
	}
	if headers := r.routeHeadersForProviderConfig(providerConfig); len(headers) > 0 {
		auth = route.Chain{auth, route.Headers(headers)}
	}
	capabilities := capabilitiesForRevision(effectiveRevision)
	httpClient, err := r.httpClientForProviderConfig(providerConfig)
	if err != nil {
		return model.ResolvedClient{}, resolverError(
			model.ErrorKindInvalidRequest,
			"invalid_model_provider_transport",
			"The configured model provider transport settings are invalid.",
			err,
		)
	}
	resolved := model.ResolvedClient{
		ConfiguredModelRevisionID: revision.ID.String(),
	}
	switch providerConfig.APIFormat {
	case modelprotocol.APIFormatOpenAIResponses:
		resolved.Client = openairesponses.Client{
			ModelProviderConfigID: providerConfig.ID.String(),
			Auth:                  auth,
			BaseURL:               providerConfig.BaseURL,
			EndpointPath:          providerConfig.EndpointPath,
			ProviderModelSlug:     revision.ProviderModelSlug,
			HTTPClient:            httpClient,
			ModelCapabilities:     capabilities,
			APIVariant:            providerConfig.APIVariant,
			APIVariantOptions:     revision.APIVariantOptions,
		}
		return resolved, nil
	case modelprotocol.APIFormatOpenAIChatCompletions:
		resolved.Client = openaichatcompletions.Client{
			ModelProviderConfigID: providerConfig.ID.String(),
			Auth:                  auth,
			BaseURL:               providerConfig.BaseURL,
			EndpointPath:          providerConfig.EndpointPath,
			ProviderModelSlug:     revision.ProviderModelSlug,
			HTTPClient:            httpClient,
			ModelCapabilities:     capabilities,
			APIVariant:            providerConfig.APIVariant,
			APIVariantOptions:     revision.APIVariantOptions,
		}
		return resolved, nil
	case modelprotocol.APIFormatAnthropicMessages:
		resolved.Client = anthropicmessages.Client{
			ModelProviderConfigID: providerConfig.ID.String(),
			Auth:                  auth,
			BaseURL:               providerConfig.BaseURL,
			EndpointPath:          providerConfig.EndpointPath,
			ProviderModelSlug:     revision.ProviderModelSlug,
			HTTPClient:            httpClient,
			ModelCapabilities:     capabilities,
			APIVariant:            providerConfig.APIVariant,
			APIVariantOptions:     revision.APIVariantOptions,
		}
		return resolved, nil
	default:
		return model.ResolvedClient{}, resolverError(
			model.ErrorKindInvalidRequest,
			"unsupported_model_api_format",
			"The configured model provider uses an unsupported API format.",
			fmt.Errorf("unsupported api_format %q", providerConfig.APIFormat),
		)
	}
}

func resolverError(kind model.ErrorKind, code, message string, cause error) error {
	return model.ProviderError{
		Kind:    kind,
		Source:  "model_resolver",
		Code:    code,
		Message: message,
		Cause:   cause,
	}
}

func (r Resolver) routeHeadersForProviderConfig(
	providerConfig modelstore.ModelProviderConfigRecord,
) map[string]string {
	if providerConfig.APIVariant != modelprotocol.APIVariantOpenRouter {
		return map[string]string{}
	}
	return r.OpenRouterAttribution.headers()
}

func (a OpenRouterAttribution) headers() map[string]string {
	headers := map[string]string{}
	if a.SiteURL != "" {
		headers["HTTP-Referer"] = a.SiteURL
	}
	if a.AppTitle != "" {
		headers["X-OpenRouter-Title"] = a.AppTitle
	}
	if len(a.AppCategories) > 0 {
		headers["X-OpenRouter-Categories"] = strings.Join(a.AppCategories, ",")
	}
	if len(headers) == 0 {
		return nil
	}
	return headers
}

func routeAuthForProviderConfig(
	providerConfig modelstore.ModelProviderConfigRecord,
	apiKey string,
) (route.Auth, error) {
	switch providerConfig.AuthKind {
	case modelstore.ModelProviderAuthKindBearerToken:
		if err := modelstore.ValidateModelProviderAuth(providerConfig.AuthKind, providerConfig.AuthOptions); err != nil {
			return nil, fmt.Errorf("invalid auth_options for model provider config %s: %w", providerConfig.ID, err)
		}
		return route.BearerToken{Token: apiKey}, nil
	case modelstore.ModelProviderAuthKindAPIKeyHeader:
		headerName, err := modelstore.ModelProviderAPIKeyHeaderName(providerConfig.AuthOptions)
		if err != nil {
			return nil, fmt.Errorf("invalid auth_options for model provider config %s: %w", providerConfig.ID, err)
		}
		return route.HeaderAuth{Header: headerName, Value: apiKey}, nil
	default:
		return nil, fmt.Errorf("unsupported model provider auth_kind %q", providerConfig.AuthKind)
	}
}

func (r Resolver) sigV4RouteAuth(
	providerConfig modelstore.ModelProviderConfigRecord,
	credential secretstore.SecretPayloadRecord,
) (route.Auth, error) {
	service, region, err := modelstore.ModelProviderSigV4ServiceRegion(providerConfig.AuthOptions)
	if err != nil {
		return nil, fmt.Errorf("invalid auth_options for model provider config %s: %w", providerConfig.ID, err)
	}
	provider, err := sigv4.ResolveCredentialProvider(
		r.SigV4CredentialCache,
		providerConfig.CredentialSecretID,
		credential.CurrentVersionID,
		region,
		credential.Payload,
	)
	if err != nil {
		return nil, fmt.Errorf("resolve AWS credentials for model provider config %s: %w", providerConfig.ID, err)
	}
	signer, err := sigv4.NewSigner(service, region, provider)
	if err != nil {
		return nil, fmt.Errorf("configure SigV4 for model provider config %s: %w", providerConfig.ID, err)
	}
	return signer, nil
}

func (r Resolver) httpClientForProviderConfig(
	providerConfig modelstore.ModelProviderConfigRecord,
) (*http.Client, error) {
	base := newSSRFHTTPClient(r.AllowLoopback)
	base.Timeout = time.Duration(providerConfig.RequestTimeoutMS) * time.Millisecond
	if r.HTTPRecorder == nil {
		return base, nil
	}
	return metrics.NewObservedHTTPClient(
		base,
		r.HTTPRecorder,
		metrics.WithHTTPClientPathLabel(providerConfig.EndpointPath),
	), nil
}

func newSSRFHTTPClient(allowLoopback bool) *http.Client {
	if allowLoopback {
		return &http.Client{Transport: loopbackHTTPTransport}
	}
	return &http.Client{Transport: defaultHTTPTransport}
}

func capabilitiesForRevision(record modelstore.ConfiguredModelRevisionRecord) model.Capabilities {
	supportsTools := record.SupportsTools
	return model.Capabilities{
		ContextWindowTokens:       record.ContextWindowTokens,
		MaxOutputTokens:           record.MaxOutputTokens,
		DefaultMaxOutputTokens:    valueOrZero(record.DefaultMaxOutputTokens),
		DefaultCacheRetention:     cacheRetentionForModel(record.DefaultCacheRetention),
		SupportsTools:             &supportsTools,
		SupportsReasoning:         record.SupportsReasoning,
		DefaultReasoningEffort:    record.DefaultReasoningEffort,
		SupportedReasoningEfforts: append([]string(nil), record.SupportedReasoningEfforts...),
		InputModalities:           append([]string(nil), record.InputModalities...),
		OutputModalities:          append([]string(nil), record.OutputModalities...),
	}
}

func cacheRetentionForModel(value string) model.CacheRetention {
	switch value {
	case "":
		return model.CacheRetentionUnset
	case modelstore.ModelCacheRetentionShort:
		return model.CacheRetentionShort
	case modelstore.ModelCacheRetentionLong:
		return model.CacheRetentionLong
	default:
		return model.CacheRetentionNone
	}
}

func valueOrZero(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}
