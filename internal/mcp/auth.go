package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"github.com/omnara-ai/omnara/internal/outboundhttp"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/urlpolicy"
	"golang.org/x/oauth2"
)

const (
	maxOAuthResponseBodyBytes = 1024 * 1024
	maxOAuthErrorCodeBytes    = 128
	oauthBearerTokenType      = "Bearer"

	oauthStatePurpose  = "mcp-oauth-state"
	maxOAuthStateBytes = 4096
)

type AuthOptions struct {
	HTTPClient *http.Client
}

type AuthRequirement struct {
	Required                  bool
	EndpointURL               string
	Resource                  string
	Scopes                    []string
	ProtectedResourceMetadata *oauthex.ProtectedResourceMetadata
	AuthorizationServer       *oauthex.AuthServerMeta
}

type OAuthStartInput struct {
	ClientID     string
	ClientSecret string
	RedirectURI  string
	Scopes       []string
	AuthMetadata *AuthRequirement
	FlowData     json.RawMessage
	ExpiresAt    time.Time
	KeyWrapper   secrets.KeyWrapper
}

type OAuthFlowState struct {
	FlowData      json.RawMessage `json:"flow_data,omitempty"`
	EndpointURL   string          `json:"endpoint_url"`
	Resource      string          `json:"resource"`
	TokenEndpoint string          `json:"token_endpoint"`
	ClientID      string          `json:"client_id"`
	ClientSecret  string          `json:"client_secret,omitempty"`
	CodeVerifier  string          `json:"code_verifier"`
	Scopes        []string        `json:"scopes,omitempty"`
	ExpiresAt     time.Time       `json:"expires_at"`
}

type OAuthCodeExchangeInput struct {
	TokenEndpoint string
	ClientID      string
	ClientSecret  string
	RedirectURI   string
	Code          string
	CodeVerifier  string
	Resource      string
	HTTPClient    *http.Client
	AuthStyle     oauth2.AuthStyle
}

type OAuthRefreshInput struct {
	TokenEndpoint string
	ClientID      string
	ClientSecret  string
	RefreshToken  string
	Resource      string
	HTTPClient    *http.Client
	AuthStyle     oauth2.AuthStyle
}

type OAuthTokenSet struct {
	AccessToken     string
	RefreshToken    string
	IDToken         string
	TokenType       string
	Scopes          []string
	ExpiresIn       time.Duration
	lifetimeStarted time.Time
}

func (t OAuthTokenSet) AccessTokenLifetime() secrets.OAuthAccessTokenLifetime {
	return secrets.NewOAuthAccessTokenLifetime(t.ExpiresIn, t.lifetimeStarted)
}

func DetectAuth(ctx context.Context, endpoint string, opts AuthOptions) (AuthRequirement, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return AuthRequirement{}, errors.New("mcp auth: endpoint URL is required")
	}
	if err := urlpolicy.RequireHTTPSOrLoopback(endpoint); err != nil {
		return AuthRequirement{}, fmt.Errorf("mcp auth: endpoint URL: %w", err)
	}
	client := clientWithoutRedirects(opts.HTTPClient)
	response, err := probeMCPAuth(ctx, endpoint, client)
	if err != nil {
		return AuthRequirement{}, err
	}
	defer response.Body.Close() //nolint:errcheck // Nothing actionable if closing a response body fails here.

	if response.StatusCode != http.StatusUnauthorized {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxOAuthResponseBodyBytes))
		if response.StatusCode < 200 || response.StatusCode > 299 {
			return AuthRequirement{}, fmt.Errorf(
				"mcp auth: initialize probe returned unexpected HTTP %d",
				response.StatusCode,
			)
		}
		return AuthRequirement{Required: false, EndpointURL: endpoint, Resource: canonicalResourceURI(endpoint)}, nil
	}
	authRequirement, err := authRequirementFromChallenge(
		ctx,
		endpoint,
		response.Header.Values("WWW-Authenticate"),
		client,
	)
	if err != nil {
		return AuthRequirement{}, fmt.Errorf("%w: %w", ErrOAuthMetadataUnavailable, err)
	}
	authRequirement.Required = true
	return authRequirement, nil
}

func StartOAuth(ctx context.Context, input OAuthStartInput) (string, error) {
	if strings.TrimSpace(input.ClientID) == "" {
		return "", errors.New("mcp auth: client ID is required")
	}
	if strings.TrimSpace(input.RedirectURI) == "" {
		return "", errors.New("mcp auth: redirect URI is required")
	}
	if input.AuthMetadata == nil || input.AuthMetadata.AuthorizationServer == nil {
		return "", errors.New("mcp auth: authorization server metadata is required")
	}
	if input.KeyWrapper == nil {
		return "", errors.New("mcp auth: key wrapper is required")
	}
	if input.ExpiresAt.IsZero() {
		return "", errors.New("mcp auth: flow expiry is required")
	}
	authMetadata := *input.AuthMetadata
	if authMetadata.Resource == "" {
		authMetadata.Resource = canonicalResourceURI(authMetadata.EndpointURL)
	}
	codeVerifier := oauth2.GenerateVerifier()
	flow := OAuthFlowState{
		FlowData:      input.FlowData,
		EndpointURL:   authMetadata.EndpointURL,
		Resource:      authMetadata.Resource,
		TokenEndpoint: authMetadata.AuthorizationServer.TokenEndpoint,
		ClientID:      input.ClientID,
		ClientSecret:  input.ClientSecret,
		CodeVerifier:  codeVerifier,
		Scopes:        append([]string(nil), input.Scopes...),
		ExpiresAt:     input.ExpiresAt.UTC(),
	}
	state, err := sealOAuthState(ctx, input.KeyWrapper, flow)
	if err != nil {
		return "", err
	}
	oauthAuthorizationConfig := oauth2.Config{
		ClientID:    input.ClientID,
		RedirectURL: input.RedirectURI,
		Endpoint: oauth2.Endpoint{
			AuthURL:  authMetadata.AuthorizationServer.AuthorizationEndpoint,
			TokenURL: authMetadata.AuthorizationServer.TokenEndpoint,
		},
		Scopes: flow.Scopes,
	}
	return oauthAuthorizationConfig.AuthCodeURL(state,
		oauth2.S256ChallengeOption(codeVerifier),
		oauth2.SetAuthURLParam("resource", authMetadata.Resource),
	), nil
}

func sealOAuthState(ctx context.Context, keyWrapper secrets.KeyWrapper, flow OAuthFlowState) (string, error) {
	plaintext, err := json.Marshal(flow)
	if err != nil {
		return "", fmt.Errorf("mcp auth: marshal oauth flow state: %w", err)
	}
	if len(plaintext) > maxOAuthStateBytes {
		return "", ErrOAuthStateTooLarge
	}
	state, err := secrets.SealToken(ctx, keyWrapper, oauthStatePurpose, plaintext)
	if err != nil {
		return "", fmt.Errorf("mcp auth: seal oauth flow state: %w", err)
	}
	return state, nil
}

func OpenOAuthState(ctx context.Context, keyWrapper secrets.KeyWrapper, state string) (OAuthFlowState, error) {
	plaintext, err := secrets.OpenToken(ctx, keyWrapper, oauthStatePurpose, state)
	if err != nil {
		return OAuthFlowState{}, fmt.Errorf("mcp auth: open oauth state: %w", err)
	}
	var flow OAuthFlowState
	if err := json.Unmarshal(plaintext, &flow); err != nil {
		return OAuthFlowState{}, fmt.Errorf("mcp auth: parse oauth flow state: %w", err)
	}
	if !flow.ExpiresAt.After(time.Now().UTC()) {
		return OAuthFlowState{}, errors.New("mcp auth: oauth state expired")
	}
	return flow, nil
}

func ExchangeOAuthCode(ctx context.Context, input OAuthCodeExchangeInput) (OAuthTokenSet, error) {
	if strings.TrimSpace(input.TokenEndpoint) == "" {
		return OAuthTokenSet{}, errors.New("mcp auth: token endpoint is required")
	}
	if strings.TrimSpace(input.ClientID) == "" {
		return OAuthTokenSet{}, errors.New("mcp auth: client ID is required")
	}
	if strings.TrimSpace(input.RedirectURI) == "" {
		return OAuthTokenSet{}, errors.New("mcp auth: redirect URI is required")
	}
	if strings.TrimSpace(input.Code) == "" {
		return OAuthTokenSet{}, errors.New("mcp auth: authorization code is required")
	}
	if strings.TrimSpace(input.CodeVerifier) == "" {
		return OAuthTokenSet{}, errors.New("mcp auth: code verifier is required")
	}
	if strings.TrimSpace(input.Resource) == "" {
		return OAuthTokenSet{}, errors.New("mcp auth: resource is required")
	}
	values := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {input.Code},
		"redirect_uri":  {input.RedirectURI},
		"code_verifier": {input.CodeVerifier},
		"resource":      {input.Resource},
	}
	token, err := retrieveOAuthToken(
		ctx,
		input.TokenEndpoint,
		input.ClientID,
		input.ClientSecret,
		values,
		input.AuthStyle,
		input.HTTPClient,
	)
	if err != nil {
		return OAuthTokenSet{}, fmt.Errorf("mcp auth: exchange authorization code: %w", err)
	}
	return token, nil
}

func RefreshOAuthToken(ctx context.Context, input OAuthRefreshInput) (OAuthTokenSet, error) {
	if strings.TrimSpace(input.TokenEndpoint) == "" {
		return OAuthTokenSet{}, errors.New("mcp auth: token endpoint is required")
	}
	if strings.TrimSpace(input.ClientID) == "" {
		return OAuthTokenSet{}, errors.New("mcp auth: client ID is required")
	}
	if strings.TrimSpace(input.RefreshToken) == "" {
		return OAuthTokenSet{}, errors.New("mcp auth: refresh token is required")
	}
	if strings.TrimSpace(input.Resource) == "" {
		return OAuthTokenSet{}, errors.New("mcp auth: resource is required")
	}
	values := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {input.RefreshToken},
		"resource":      {input.Resource},
	}
	refreshedToken, err := retrieveOAuthToken(
		ctx,
		input.TokenEndpoint,
		input.ClientID,
		input.ClientSecret,
		values,
		input.AuthStyle,
		input.HTTPClient,
	)
	if err != nil {
		return OAuthTokenSet{}, fmt.Errorf("mcp auth: refresh token: %w", err)
	}
	if refreshedToken.RefreshToken == "" {
		refreshedToken.RefreshToken = input.RefreshToken
	}
	return refreshedToken, nil
}

func probeMCPAuth(ctx context.Context, endpoint string, client *http.Client) (*http.Response, error) {
	requestBody, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      initializeRequestID,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]string{"name": "omnara-mcp-auth-probe", "version": "v0"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("mcp auth: marshal initialize request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(requestBody))
	if err != nil {
		return nil, fmt.Errorf("mcp auth: build probe request: %w", err)
	}
	request.Header.Set("Content-Type", mediaTypeJSON)
	request.Header.Set("Accept", acceptHeader)
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("mcp auth: send probe request: %w", err)
	}
	return response, nil
}

func authRequirementFromChallenge(
	ctx context.Context,
	endpoint string,
	headers []string,
	client *http.Client,
) (AuthRequirement, error) {
	challenges, err := oauthex.ParseWWWAuthenticate(headers)
	if err != nil {
		return AuthRequirement{}, fmt.Errorf("mcp auth: parse WWW-Authenticate: %w", err)
	}
	protectedResourceMetadata, err := discoverProtectedResourceMetadata(ctx, endpoint, challenges, client)
	if err != nil {
		return AuthRequirement{}, err
	}
	authorizationServerMetadata, err := discoverAuthorizationServerMetadata(ctx, protectedResourceMetadata, client)
	if err != nil {
		return AuthRequirement{}, err
	}
	scopes := scopesFromChallenges(challenges)
	if len(scopes) == 0 {
		scopes = append([]string(nil), protectedResourceMetadata.ScopesSupported...)
	}
	return AuthRequirement{
		EndpointURL:               endpoint,
		Resource:                  protectedResourceMetadata.Resource,
		Scopes:                    scopes,
		ProtectedResourceMetadata: protectedResourceMetadata,
		AuthorizationServer:       authorizationServerMetadata,
	}, nil
}

func discoverProtectedResourceMetadata(
	ctx context.Context,
	endpoint string,
	challenges []oauthex.Challenge,
	client *http.Client,
) (*oauthex.ProtectedResourceMetadata, error) {
	var firstDiscoveryError error
	for _, candidate := range protectedResourceMetadataURLs(resourceMetadataURLFromChallenges(challenges), endpoint) {
		if err := urlpolicy.RequireHTTPSOrLoopback(candidate.URL); err != nil {
			if firstDiscoveryError == nil {
				firstDiscoveryError = fmt.Errorf("metadata URL: %w", err)
			}
			continue
		}
		protectedResourceMetadata, err := oauthex.GetProtectedResourceMetadata(
			ctx,
			candidate.URL,
			candidate.Resource,
			client,
		)
		if err != nil {
			if firstDiscoveryError == nil {
				firstDiscoveryError = err
			}
			continue
		}
		if protectedResourceMetadata == nil {
			continue
		}
		if len(protectedResourceMetadata.AuthorizationServers) == 0 {
			return nil, errors.New("mcp auth: protected resource metadata has no authorization servers")
		}
		return protectedResourceMetadata, nil
	}
	if firstDiscoveryError != nil {
		return nil, fmt.Errorf("mcp auth: discover protected resource metadata: %w", firstDiscoveryError)
	}
	return nil, errors.New("mcp auth: protected resource metadata not found")
}

func discoverAuthorizationServerMetadata(
	ctx context.Context,
	protectedResourceMetadata *oauthex.ProtectedResourceMetadata,
	client *http.Client,
) (*oauthex.AuthServerMeta, error) {
	var firstDiscoveryError error
	for _, issuer := range protectedResourceMetadata.AuthorizationServers {
		authorizationServerMetadata, err := authServerMetadataForIssuer(ctx, issuer, client)
		if err != nil {
			if firstDiscoveryError == nil {
				firstDiscoveryError = err
			}
			continue
		}
		return authorizationServerMetadata, nil
	}
	if firstDiscoveryError != nil {
		return nil, fmt.Errorf("mcp auth: discover authorization server metadata: %w", firstDiscoveryError)
	}
	return nil, errors.New("mcp auth: authorization server metadata not found")
}

func authServerMetadataForIssuer(
	ctx context.Context,
	issuer string,
	client *http.Client,
) (*oauthex.AuthServerMeta, error) {
	var firstDiscoveryError error
	for _, metadataURL := range authorizationServerMetadataURLs(issuer) {
		authorizationServerMetadata, err := fetchAuthServerMetadata(ctx, metadataURL, issuer, client)
		if errors.Is(err, errAuthServerMetadataNotFound) {
			continue
		}
		if err != nil {
			if firstDiscoveryError == nil {
				firstDiscoveryError = err
			}
			continue
		}
		if !supportsPKCES256(authorizationServerMetadata) {
			if firstDiscoveryError == nil {
				firstDiscoveryError = fmt.Errorf("authorization server %q does not advertise PKCE S256 support", issuer)
			}
			continue
		}
		if authorizationServerMetadata.AuthorizationEndpoint == "" || authorizationServerMetadata.TokenEndpoint == "" {
			if firstDiscoveryError == nil {
				firstDiscoveryError = fmt.Errorf(
					"authorization server %q metadata is missing authorization or token endpoint",
					issuer,
				)
			}
			continue
		}
		return authorizationServerMetadata, nil
	}
	if firstDiscoveryError != nil {
		return nil, fmt.Errorf("fetch authorization server metadata for %q: %w", issuer, firstDiscoveryError)
	}
	return nil, fmt.Errorf("authorization server %q returned no metadata", issuer)
}

func authorizationServerMetadataURLs(issuer string) []string {
	baseURL, err := url.Parse(issuer)
	if err != nil {
		return nil
	}
	var urls []string
	pathSuffix := strings.Trim(baseURL.Path, "/")
	if pathSuffix == "" {
		baseURL.Path = "/.well-known/oauth-authorization-server"
		urls = append(urls, baseURL.String())
		baseURL.Path = "/.well-known/openid-configuration"
		urls = append(urls, baseURL.String())
		return urls
	}
	baseURL.Path = "/.well-known/oauth-authorization-server/" + pathSuffix
	urls = append(urls, baseURL.String())
	baseURL.Path = "/.well-known/openid-configuration/" + pathSuffix
	urls = append(urls, baseURL.String())
	baseURL.Path = "/" + pathSuffix + "/.well-known/openid-configuration"
	urls = append(urls, baseURL.String())
	return urls
}

func fetchAuthServerMetadata(
	ctx context.Context,
	metadataURL string,
	issuer string,
	client *http.Client,
) (*oauthex.AuthServerMeta, error) {
	if err := urlpolicy.RequireHTTPSOrLoopback(metadataURL); err != nil {
		return nil, fmt.Errorf("metadataURL: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build metadata request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close() //nolint:errcheck // Response body close errors are not actionable here.
	if response.StatusCode >= 400 && response.StatusCode < 500 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxOAuthResponseBodyBytes))
		return nil, errAuthServerMetadataNotFound
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxOAuthResponseBodyBytes))
		return nil, fmt.Errorf("metadata endpoint returned HTTP %d", response.StatusCode)
	}
	body, err := readOAuthResponseBody(response.Body)
	if err != nil {
		return nil, fmt.Errorf("read metadata response: %w", err)
	}
	var metadata oauthex.AuthServerMeta
	if err := json.Unmarshal(body, &metadata); err != nil {
		return nil, fmt.Errorf("decode metadata response: %w", err)
	}
	if metadata.Issuer != issuer && !isMultiTenantIssuerAlias(metadata.Issuer, issuer) {
		return nil, fmt.Errorf("metadata issuer %q does not match issuer URL %q", metadata.Issuer, issuer)
	}
	if err := validateAuthServerMetadataURLs(metadata); err != nil {
		return nil, err
	}
	return &metadata, nil
}

func validateAuthServerMetadataURLs(metadata oauthex.AuthServerMeta) error {
	required := map[string]string{
		"authorization_endpoint": metadata.AuthorizationEndpoint,
		"token_endpoint":         metadata.TokenEndpoint,
	}
	for name, value := range required {
		if value == "" {
			continue
		}
		if err := urlpolicy.RequireHTTPSOrLoopback(value); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	optional := map[string]string{
		"jwks_uri":               metadata.JWKSURI,
		"registration_endpoint":  metadata.RegistrationEndpoint,
		"revocation_endpoint":    metadata.RevocationEndpoint,
		"introspection_endpoint": metadata.IntrospectionEndpoint,
	}
	for name, value := range optional {
		if value == "" {
			continue
		}
		if err := urlpolicy.RequireHTTPSOrLoopback(value); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

// multiTenantIssuerAliases are the Microsoft Entra ID (Azure AD) shared
// tenant path segments whose discovery document reports a tenant-specific
// issuer instead of echoing the requested issuer verbatim, e.g. requesting
// ".../common/v2.0" resolves to ".../{tenant-id}/v2.0" on the same origin.
var multiTenantIssuerAliases = map[string]bool{
	"common":        true,
	"organizations": true,
	"consumers":     true,
}

// isMultiTenantIssuerAlias reports whether metadataIssuer is a same-origin,
// tenant-scoped alias of a Microsoft Entra ID multi-tenant issuer. This is
// the only accepted deviation from an exact RFC 8414 issuer match: every
// other mismatch, including any other same-origin path difference, is
// rejected so a client can't be bound to the wrong authorization server.
func isMultiTenantIssuerAlias(metadataIssuer, requestedIssuer string) bool {
	metadataURL, err := url.Parse(metadataIssuer)
	if err != nil {
		return false
	}
	requestedURL, err := url.Parse(requestedIssuer)
	if err != nil {
		return false
	}
	if !strings.EqualFold(metadataURL.Scheme, requestedURL.Scheme) ||
		!strings.EqualFold(metadataURL.Host, requestedURL.Host) {
		return false
	}
	requestedSegments := strings.Split(strings.Trim(requestedURL.Path, "/"), "/")
	metadataSegments := strings.Split(strings.Trim(metadataURL.Path, "/"), "/")
	if len(requestedSegments) == 0 || len(requestedSegments) != len(metadataSegments) {
		return false
	}
	if !multiTenantIssuerAliases[strings.ToLower(requestedSegments[0])] {
		return false
	}
	if metadataSegments[0] == "" || multiTenantIssuerAliases[strings.ToLower(metadataSegments[0])] {
		return false
	}
	for i := 1; i < len(requestedSegments); i++ {
		if requestedSegments[i] != metadataSegments[i] {
			return false
		}
	}
	return true
}

type protectedResourceMetadataURL struct {
	URL      string
	Resource string
}

func protectedResourceMetadataURLs(metadataURL, resourceURL string) []protectedResourceMetadataURL {
	canonicalResource := canonicalResourceURI(resourceURL)
	var candidates []protectedResourceMetadataURL
	if metadataURL != "" {
		candidates = append(candidates, protectedResourceMetadataURL{URL: metadataURL, Resource: canonicalResource})
	}
	resourceEndpointURL, err := url.Parse(resourceURL)
	if err != nil {
		return candidates
	}

	endpointMetadataURL := *resourceEndpointURL
	endpointMetadataURL.Path = "/.well-known/oauth-protected-resource/" + strings.TrimLeft(
		resourceEndpointURL.Path,
		"/",
	)
	endpointMetadataURL.RawQuery = ""
	endpointMetadataURL.Fragment = ""
	candidates = append(
		candidates,
		protectedResourceMetadataURL{URL: endpointMetadataURL.String(), Resource: canonicalResource},
	)

	rootResourceURL := *resourceEndpointURL
	rootResourceURL.Path = ""
	rootResourceURL.RawQuery = ""
	rootResourceURL.Fragment = ""

	rootMetadataURL := *resourceEndpointURL
	rootMetadataURL.Path = "/.well-known/oauth-protected-resource"
	rootMetadataURL.RawQuery = ""
	rootMetadataURL.Fragment = ""
	candidates = append(
		candidates,
		protectedResourceMetadataURL{
			URL:      rootMetadataURL.String(),
			Resource: canonicalResourceURI(rootResourceURL.String()),
		},
	)
	return candidates
}

func resourceMetadataURLFromChallenges(challenges []oauthex.Challenge) string {
	for _, challenge := range challenges {
		if strings.EqualFold(challenge.Scheme, "bearer") && challenge.Params["resource_metadata"] != "" {
			return challenge.Params["resource_metadata"]
		}
	}
	return ""
}

func scopesFromChallenges(challenges []oauthex.Challenge) []string {
	for _, challenge := range challenges {
		if strings.EqualFold(challenge.Scheme, "bearer") && challenge.Params["scope"] != "" {
			return strings.Fields(challenge.Params["scope"])
		}
	}
	return nil
}

func supportsPKCES256(metadata *oauthex.AuthServerMeta) bool {
	if metadata == nil {
		return false
	}
	for _, method := range metadata.CodeChallengeMethodsSupported {
		if method == "S256" {
			return true
		}
	}
	return false
}

func canonicalResourceURI(rawResource string) string {
	resourceURL, err := url.Parse(rawResource)
	if err != nil {
		return rawResource
	}
	resourceURL.Scheme = strings.ToLower(resourceURL.Scheme)
	resourceURL.Host = strings.ToLower(resourceURL.Host)
	resourceURL.Fragment = ""
	if resourceURL.Path == "/" {
		resourceURL.Path = ""
	}
	return resourceURL.String()
}

func retrieveOAuthToken(
	ctx context.Context,
	tokenEndpoint, clientID, clientSecret string,
	tokenRequestValues url.Values,
	authStyle oauth2.AuthStyle,
	client *http.Client,
) (OAuthTokenSet, error) {
	if err := urlpolicy.RequireHTTPSOrLoopback(tokenEndpoint); err != nil {
		return OAuthTokenSet{}, fmt.Errorf("token endpoint: %w", err)
	}
	client = clientWithoutRedirects(client)
	if authStyle != oauth2.AuthStyleAutoDetect {
		return retrieveOAuthTokenOnce(ctx, tokenEndpoint, clientID, clientSecret, tokenRequestValues, authStyle, client)
	}
	token, err := retrieveOAuthTokenOnce(
		ctx,
		tokenEndpoint,
		clientID,
		clientSecret,
		tokenRequestValues,
		oauth2.AuthStyleInHeader,
		client,
	)
	if err == nil || !isClientAuthStyleRejection(err) {
		return token, err
	}
	return retrieveOAuthTokenOnce(
		ctx,
		tokenEndpoint,
		clientID,
		clientSecret,
		tokenRequestValues,
		oauth2.AuthStyleInParams,
		client,
	)
}

func isClientAuthStyleRejection(err error) bool {
	var endpointError *tokenEndpointError
	if !errors.As(err, &endpointError) {
		return false
	}
	if endpointError.statusCode == http.StatusUnauthorized {
		return true
	}
	return endpointError.code == "invalid_client"
}

type tokenEndpointError struct {
	statusCode int
	code       string
	cause      error
}

func (e *tokenEndpointError) Error() string {
	if e.code != "" {
		return fmt.Sprintf("token endpoint returned HTTP %d with OAuth error %q", e.statusCode, e.code)
	}
	if e.cause != nil {
		return fmt.Sprintf("token endpoint returned HTTP %d: %v", e.statusCode, e.cause)
	}
	return fmt.Sprintf("token endpoint returned HTTP %d", e.statusCode)
}

func (e *tokenEndpointError) Unwrap() error {
	return e.cause
}

func newTokenEndpointError(statusCode int, body []byte) *tokenEndpointError {
	var response struct {
		Code string `json:"error"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return &tokenEndpointError{statusCode: statusCode}
	}
	return &tokenEndpointError{
		statusCode: statusCode,
		code:       sanitizeOAuthErrorCode(response.Code),
	}
}

func sanitizeOAuthErrorCode(code string) string {
	if code == "" || len(code) > maxOAuthErrorCodeBytes {
		return ""
	}
	for i := range len(code) {
		char := code[i]
		if char < 0x20 || char == 0x22 || char == 0x5c || char > 0x7e {
			return ""
		}
	}
	return code
}

func retrieveOAuthTokenOnce(
	ctx context.Context,
	tokenEndpoint, clientID, clientSecret string,
	tokenRequestValues url.Values,
	authStyle oauth2.AuthStyle,
	client *http.Client,
) (OAuthTokenSet, error) {
	started := time.Now()
	requestForm := cloneValues(tokenRequestValues)
	if authStyle == oauth2.AuthStyleInParams || clientSecret == "" {
		requestForm.Set("client_id", clientID)
		if clientSecret != "" {
			requestForm.Set("client_secret", clientSecret)
		}
	}
	tokenRequest, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		tokenEndpoint,
		strings.NewReader(requestForm.Encode()),
	)
	if err != nil {
		return OAuthTokenSet{}, err
	}
	tokenRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tokenRequest.Header.Set("Accept", "application/json")
	if authStyle == oauth2.AuthStyleInHeader && clientSecret != "" {
		tokenRequest.SetBasicAuth(url.QueryEscape(clientID), url.QueryEscape(clientSecret))
	}
	tokenResponse, err := client.Do(tokenRequest)
	if err != nil {
		return OAuthTokenSet{}, err
	}
	defer tokenResponse.Body.Close() //nolint:errcheck // Nothing actionable if closing a response body fails here.
	successfulResponse := tokenResponse.StatusCode >= 200 && tokenResponse.StatusCode <= 299
	body, err := readOAuthResponseBody(tokenResponse.Body)
	if err != nil {
		if !successfulResponse && errors.Is(err, ErrResponseTooLarge) {
			return OAuthTokenSet{}, &tokenEndpointError{
				statusCode: tokenResponse.StatusCode,
				cause:      err,
			}
		}
		return OAuthTokenSet{}, fmt.Errorf("read token endpoint response: %w", err)
	}
	if !successfulResponse {
		return OAuthTokenSet{}, newTokenEndpointError(tokenResponse.StatusCode, body)
	}
	token, err := parseOAuthTokenResponse(body)
	if err != nil {
		return OAuthTokenSet{}, err
	}
	token.lifetimeStarted = started
	if _, err := token.AccessTokenLifetime().Remaining(); err != nil {
		return OAuthTokenSet{}, err
	}
	return token, nil
}

func readOAuthResponseBody(body io.Reader) ([]byte, error) {
	response, err := io.ReadAll(io.LimitReader(body, maxOAuthResponseBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(response) > maxOAuthResponseBodyBytes {
		return nil, ErrResponseTooLarge
	}
	return response, nil
}

func parseOAuthTokenResponse(body []byte) (OAuthTokenSet, error) {
	var tokenResponse struct {
		AccessToken  string           `json:"access_token"`
		TokenType    string           `json:"token_type"`
		RefreshToken string           `json:"refresh_token"`
		IDToken      string           `json:"id_token"`
		Scope        string           `json:"scope"`
		ExpiresIn    expiresInSeconds `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tokenResponse); err != nil {
		return OAuthTokenSet{}, fmt.Errorf("parse token response: %w", err)
	}
	token := OAuthTokenSet{
		AccessToken:  tokenResponse.AccessToken,
		TokenType:    tokenResponse.TokenType,
		RefreshToken: tokenResponse.RefreshToken,
		IDToken:      tokenResponse.IDToken,
		Scopes:       strings.Fields(tokenResponse.Scope),
	}
	if tokenResponse.ExpiresIn > 0 {
		token.ExpiresIn = time.Duration(tokenResponse.ExpiresIn) * time.Second
	}
	if err := validateOAuthToken(&token); err != nil {
		return OAuthTokenSet{}, err
	}
	return token, nil
}

type expiresInSeconds int64

func (s *expiresInSeconds) UnmarshalJSON(data []byte) error {
	value := strings.TrimSpace(string(data))
	if value == "null" {
		*s = 0
		return nil
	}
	value = strings.Trim(value, `"`)
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return fmt.Errorf("invalid expires_in %q", value)
	}
	if parsed > secrets.MaxOAuthAccessTokenTTLSeconds {
		return fmt.Errorf("expires_in %q exceeds the supported duration", value)
	}
	*s = expiresInSeconds(parsed)
	return nil
}

func validateOAuthToken(token *OAuthTokenSet) error {
	if token.AccessToken == "" {
		return errors.New("token response missing access_token")
	}
	if token.TokenType == "" {
		return errors.New("token response missing token_type")
	}
	if !strings.EqualFold(token.TokenType, oauthBearerTokenType) {
		return errors.New("token response token_type is not Bearer")
	}
	token.TokenType = oauthBearerTokenType
	return nil
}

func cloneValues(values url.Values) url.Values {
	cloned := make(url.Values, len(values))
	for key, value := range values {
		cloned[key] = append([]string(nil), value...)
	}
	return cloned
}

func clientWithoutRedirects(client *http.Client) *http.Client {
	if client == nil {
		client = outboundhttp.NewPublicClient(outboundhttp.PublicClientOptions{})
	}
	return outboundhttp.CloneRejectingRedirects(client)
}
