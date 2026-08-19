package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	logpkg "github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/mcp"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/resourcemeta"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const (
	mcpOAuthCallbackPath       = "/api/mcp-oauth/callback"
	mcpOAuthClientMetadataPath = "/.well-known/oauth-client.json"

	mcpOAuthClientName      = "Omnara"
	mcpOAuthFlowLifetime    = 10 * time.Minute
	mcpOAuthOutboundTimeout = 30 * time.Second
)

type mcpOAuthOwner struct {
	OrgID          storage.ID
	OwnerKind      string
	OwnerProjectID storage.ID
	OwnerUserID    storage.ID
}

func userPrincipalFromContext(ctx context.Context) (identitystore.PrincipalRecord, *apierror.ResponseError) {
	principal, ok := principalFromContext(ctx)
	if !ok || principal.Type != identitystore.PrincipalTypeUser {
		err := apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
		return identitystore.PrincipalRecord{}, &err
	}
	return principal, nil
}

type mcpOAuthFlowData struct {
	FlowID          storage.ID            `json:"flow_id"`
	OrgID           storage.ID            `json:"org_id"`
	OwnerKind       string                `json:"owner_kind"`
	OwnerProjectID  storage.ID            `json:"owner_project_id"`
	OwnerUserID     storage.ID            `json:"owner_user_id"`
	CreatedByUserID storage.ID            `json:"created_by_user_id"`
	SecretName      string                `json:"secret_name"`
	Metadata        resourcemeta.Metadata `json:"metadata,omitempty"`
	ReturnTo        string                `json:"return_to,omitempty"`
}

func (s strictOpenAPIServer) StartSecretMCPOAuth(
	ctx context.Context,
	request openapi.StartSecretMCPOAuthRequestObject,
) (openapi.StartSecretMCPOAuthResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	principal, principalErr := userPrincipalFromContext(ctx)
	if principalErr != nil {
		return nil, *principalErr
	}
	ownerKind, ownerProjectID, ownerUserID, ownerErr := parseSecretOwnerInput(request.Body.Owner, principal)
	if ownerErr != nil {
		return nil, *ownerErr
	}
	if err := s.server.store.Secrets().AuthorizeSecretOwnerManage(ctx, org.ID, secretstore.SecretOwner{
		Kind: ownerKind, ProjectID: ownerProjectID, UserID: ownerUserID,
	}, principal); err != nil {
		return nil, secretAPIError(ctx, err)
	}
	response, apiErr, err := s.startMCPOAuth(
		ctx,
		mcpOAuthOwner{OrgID: org.ID, OwnerKind: ownerKind, OwnerProjectID: ownerProjectID, OwnerUserID: ownerUserID},
		principal,
		request.Body,
	)
	if err != nil {
		return nil, err
	}
	if apiErr != nil {
		return nil, *apiErr
	}
	return openapi.StartSecretMCPOAuth201JSONResponse(response), nil
}

func (s strictOpenAPIServer) startMCPOAuth(
	ctx context.Context,
	owner mcpOAuthOwner,
	principal identitystore.PrincipalRecord,
	body *openapi.MCPOAuthStartRequest,
) (openapi.MCPOAuthStartResponse, *apierror.ResponseError, error) {
	if body == nil {
		err := apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
		return openapi.MCPOAuthStartResponse{}, &err, nil
	}
	if s.server.publicURL == "" || s.server.secretKeyWrapper == nil {
		err := apierror.FromCode(openapi.ErrorCodeServiceUnavailable,
			"mcp oauth requires a configured public URL and secret encryption keys")
		return openapi.MCPOAuthStartResponse{}, &err, nil
	}
	if body.Name == "" {
		err := apierror.FromCode(openapi.ErrorCodeInvalidRequest, "name is required")
		return openapi.MCPOAuthStartResponse{}, &err, nil
	}
	metadata := body.Metadata
	if err := metadata.ValidateWithReservedKey(secrets.KeyMCPURL); err != nil {
		apiErr := apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
		return openapi.MCPOAuthStartResponse{}, &apiErr, nil
	}
	clientID := ""
	if body.ClientId != nil {
		clientID = *body.ClientId
	}
	clientSecret := ""
	if body.ClientSecret != nil {
		clientSecret = *body.ClientSecret
	}
	if clientSecret != "" && clientID == "" {
		err := apierror.FromCode(openapi.ErrorCodeInvalidRequest, "client_secret requires client_id")
		return openapi.MCPOAuthStartResponse{}, &err, nil
	}
	mcpURL, err := agentconfig.ValidateMCPURL(body.McpUrl, s.server.agentConfigOptions.AllowInsecureLocalMCPHTTP)
	if err != nil {
		apiErr := apierror.FromCode(openapi.ErrorCodeInvalidRequest, "mcp_url is invalid: "+err.Error())
		return openapi.MCPOAuthStartResponse{}, &apiErr, nil
	}
	if parsedMCPURL, err := url.Parse(mcpURL); err != nil || parsedMCPURL.User != nil {
		apiErr := apierror.FromCode(openapi.ErrorCodeInvalidRequest, "mcp_url must not contain credentials")
		return openapi.MCPOAuthStartResponse{}, &apiErr, nil
	}
	outboundCtx, cancel := context.WithTimeout(ctx, mcpOAuthOutboundTimeout)
	defer cancel()
	requirement, err := mcp.DetectAuth(outboundCtx, mcpURL, mcp.AuthOptions{HTTPClient: s.server.mcpOAuthHTTPClient})
	if err != nil {
		apiErr := apierror.FromCode(openapi.ErrorCodeUpstreamError, "mcp authorization discovery failed: "+err.Error())
		return openapi.MCPOAuthStartResponse{}, &apiErr, nil
	}
	if !requirement.Required {
		apiErr := apierror.FromCode(openapi.ErrorCodeConflict, "mcp server does not require authorization")
		return openapi.MCPOAuthStartResponse{}, &apiErr, nil
	}
	var scopes []string
	if body.Scopes != nil {
		scopes = append([]string(nil), *body.Scopes...)
	}
	if len(scopes) == 0 {
		scopes = append([]string(nil), requirement.Scopes...)
	}
	clientID, clientSecret, apiErr := s.server.resolveMCPOAuthClientForAPI(
		outboundCtx,
		clientID,
		clientSecret,
		requirement,
		scopes,
	)
	if apiErr != nil {
		return openapi.MCPOAuthStartResponse{}, apiErr, nil
	}
	flowID, err := uuid.NewV7()
	if err != nil {
		logpkg.Error(ctx, fmt.Errorf("generate mcp oauth flow id: %w", err))
		apiErr := apierror.FromCode(openapi.ErrorCodeInternalError, "internal server error")
		return openapi.MCPOAuthStartResponse{}, &apiErr, nil
	}
	returnTo := ""
	if body.ReturnTo != nil {
		returnTo = *body.ReturnTo
	}
	flowData, err := json.Marshal(mcpOAuthFlowData{
		FlowID:          flowID,
		OrgID:           owner.OrgID,
		OwnerKind:       owner.OwnerKind,
		OwnerProjectID:  owner.OwnerProjectID,
		OwnerUserID:     owner.OwnerUserID,
		CreatedByUserID: principal.ID,
		SecretName:      body.Name,
		Metadata:        metadata,
		ReturnTo:        returnTo,
	})
	if err != nil {
		logpkg.Error(ctx, fmt.Errorf("marshal mcp oauth flow data: %w", err))
		apiErr := apierror.FromCode(openapi.ErrorCodeInternalError, "internal server error")
		return openapi.MCPOAuthStartResponse{}, &apiErr, nil
	}
	expiresAt := time.Now().UTC().Add(mcpOAuthFlowLifetime)
	authURL, err := mcp.StartOAuth(ctx, mcp.OAuthStartInput{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURI:  s.server.mcpOAuthCallbackURL(),
		Scopes:       scopes,
		AuthMetadata: &requirement,
		FlowData:     flowData,
		ExpiresAt:    expiresAt,
		KeyWrapper:   s.server.secretKeyWrapper,
	})
	if errors.Is(err, mcp.ErrOAuthStateTooLarge) {
		apiErr := apierror.FromCode(
			openapi.ErrorCodeInvalidRequest,
			"request fields are too large for the oauth state parameter",
		)
		return openapi.MCPOAuthStartResponse{}, &apiErr, nil
	}
	if err != nil {
		logpkg.Error(ctx, fmt.Errorf("start mcp oauth flow: %w", err))
		apiErr := apierror.FromCode(openapi.ErrorCodeInternalError, "internal server error")
		return openapi.MCPOAuthStartResponse{}, &apiErr, nil
	}
	publicFlowID, err := publicID(publicid.KindMCPOAuthFlow, flowID)
	if err != nil {
		logpkg.Error(ctx, err)
		apiErr := apierror.FromCode(openapi.ErrorCodeInternalError, "internal server error")
		return openapi.MCPOAuthStartResponse{}, &apiErr, nil
	}
	return openapi.MCPOAuthStartResponse{
		FlowId:           publicFlowID,
		AuthorizationUrl: authURL,
		ExpiresAt:        expiresAt,
	}, nil, nil
}

func (s *Server) resolveMCPOAuthClientForAPI(
	ctx context.Context,
	clientID string,
	clientSecret string,
	requirement mcp.AuthRequirement,
	scopes []string,
) (string, string, *apierror.ResponseError) {
	if clientID != "" {
		return clientID, clientSecret, nil
	}
	if clientMetadataURL, ok := s.mcpOAuthClientMetadataURL(); ok &&
		requirement.AuthorizationServer.ClientIDMetadataDocumentSupported {
		return clientMetadataURL, "", nil
	}
	if requirement.AuthorizationServer.RegistrationEndpoint != "" {
		clientMeta := &oauthex.ClientRegistrationMetadata{
			RedirectURIs:            []string{s.mcpOAuthCallbackURL()},
			TokenEndpointAuthMethod: "none",
			GrantTypes:              []string{"authorization_code", "refresh_token"},
			ResponseTypes:           []string{"code"},
			ClientName:              mcpOAuthClientName,
		}
		if len(scopes) > 0 {
			clientMeta.Scope = strings.Join(scopes, " ")
		}
		registered, err := oauthex.RegisterClient(
			ctx,
			requirement.AuthorizationServer.RegistrationEndpoint,
			clientMeta,
			s.mcpOAuthHTTPClient,
		)
		if err != nil {
			apiErr := apierror.FromCode(openapi.ErrorCodeUpstreamError, "mcp client registration failed: "+err.Error())
			return "", "", &apiErr
		}
		return registered.ClientID, registered.ClientSecret, nil
	}
	apiErr := apierror.FromCode(openapi.ErrorCodeUnprocessable,
		"authorization server supports neither client ID metadata documents nor dynamic client registration; supply client_id")
	return "", "", &apiErr
}

func (s *Server) mcpOAuthCallbackRoute(w http.ResponseWriter, r *http.Request) {
	if s.store == nil || s.publicURL == "" || s.secretKeyWrapper == nil {
		apierror.Write(w, openapi.ErrorCodeServiceUnavailable, "mcp oauth is not configured")
		return
	}
	query := r.URL.Query()
	state := query.Get("state")
	if state == "" {
		apierror.Write(w, openapi.ErrorCodeInvalidRequest, "state is required")
		return
	}
	flow, err := mcp.OpenOAuthState(r.Context(), s.secretKeyWrapper, state)
	if err != nil {
		apierror.Write(w, openapi.ErrorCodeUnauthorized, "mcp oauth state invalid or expired")
		return
	}
	var flowData mcpOAuthFlowData
	if err := json.Unmarshal(flow.FlowData, &flowData); err != nil {
		logpkg.Error(r.Context(), fmt.Errorf("parse mcp oauth flow data: %w", err))
		apierror.Write(w, openapi.ErrorCodeInternalError)
		return
	}
	if errorCode := query.Get("error"); errorCode != "" {
		s.redirectOAuthOutcome(w, r, flowData.ReturnTo, url.Values{"mcp_oauth_error": {errorCode}})
		return
	}
	code := query.Get("code")
	if code == "" {
		s.redirectOAuthOutcome(w, r, flowData.ReturnTo, url.Values{"mcp_oauth_error": {"missing_code"}})
		return
	}
	consumed, err := s.store.Secrets().MCPOAuthFlowConsumed(r.Context(), flowData.FlowID)
	if err != nil {
		logpkg.Error(r.Context(), fmt.Errorf("check mcp oauth flow consumed: %w", err))
		apierror.Write(w, openapi.ErrorCodeInternalError)
		return
	}
	if consumed {
		apierror.Write(w, openapi.ErrorCodeUnauthorized, "mcp oauth state already redeemed")
		return
	}
	outboundCtx, cancel := context.WithTimeout(r.Context(), mcpOAuthOutboundTimeout)
	defer cancel()
	token, err := mcp.ExchangeOAuthCode(outboundCtx, mcp.OAuthCodeExchangeInput{
		TokenEndpoint: flow.TokenEndpoint,
		ClientID:      flow.ClientID,
		ClientSecret:  flow.ClientSecret,
		RedirectURI:   s.mcpOAuthCallbackURL(),
		Code:          code,
		CodeVerifier:  flow.CodeVerifier,
		Resource:      flow.Resource,
		HTTPClient:    s.mcpOAuthHTTPClient,
	})
	if err != nil {
		logpkg.Error(r.Context(), fmt.Errorf("mcp oauth code exchange failed: %w", err))
		s.redirectOAuthOutcome(w, r, flowData.ReturnTo, url.Values{"mcp_oauth_error": {"exchange_failed"}})
		return
	}
	secretID, err := s.saveMCPOAuthSecret(r.Context(), flow, flowData, token)
	if errors.Is(err, storeerr.ErrMCPOAuthFlowConsumed) {
		apierror.Write(w, openapi.ErrorCodeUnauthorized, "mcp oauth state already redeemed")
		return
	}
	if err != nil {
		logpkg.Error(r.Context(), fmt.Errorf("mcp oauth secret save failed: %w", err))
		s.redirectOAuthOutcome(w, r, flowData.ReturnTo, url.Values{"mcp_oauth_error": {"secret_save_failed"}})
		return
	}
	publicSecretID, err := publicID(publicid.KindSecret, secretID)
	if err != nil {
		apierror.Write(w, openapi.ErrorCodeInternalError)
		return
	}
	s.redirectOAuthOutcome(w, r, flowData.ReturnTo, url.Values{"mcp_oauth": {"success"}, "secret_id": {publicSecretID}})
}

func (s *Server) saveMCPOAuthSecret(
	ctx context.Context,
	flow mcp.OAuthFlowState,
	flowData mcpOAuthFlowData,
	token mcp.OAuthTokenSet,
) (storage.ID, error) {
	material := secrets.OAuthTokenSetMaterial{
		AccessToken:         token.AccessToken,
		AccessTokenLifetime: token.AccessTokenLifetime(),
		IDToken:             token.IDToken,
		MCPURL:              flow.EndpointURL,
		TokenType:           token.TokenType,
	}
	if token.RefreshToken != "" {
		material.Refresh = &secrets.OAuthRefreshMaterial{
			RefreshToken:  token.RefreshToken,
			TokenEndpoint: flow.TokenEndpoint,
			ClientID:      flow.ClientID,
			ClientSecret:  flow.ClientSecret,
			Resource:      flow.Resource,
		}
	}
	scopes := token.Scopes
	if len(scopes) == 0 {
		scopes = flow.Scopes
	}
	if len(scopes) > 0 {
		material.Scopes = strings.Join(scopes, " ")
	}
	existing, err := s.store.Secrets().GetSecretByOwnerName(
		ctx,
		flowData.OrgID,
		flowData.OwnerKind,
		flowData.OwnerProjectID,
		flowData.OwnerUserID,
		flowData.SecretName,
	)
	if errors.Is(err, storeerr.ErrNotFound) {
		metadata, err := mcpOAuthSecretMetadata(nil, flowData.Metadata, flow.EndpointURL)
		if err != nil {
			return storage.NilID, err
		}
		record, _, err := s.store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
			OrgID:          flowData.OrgID,
			OwnerKind:      flowData.OwnerKind,
			OwnerProjectID: flowData.OwnerProjectID,
			OwnerUserID:    flowData.OwnerUserID,
			Name:           flowData.SecretName,
			Metadata:       metadata,
			Material:       material,
			Actor:          identitystore.NewUserPrincipal(flowData.CreatedByUserID),
			MCPOAuthFlowID: flowData.FlowID,
		})
		if err != nil {
			return storage.NilID, err
		}
		return record.ID, nil
	}
	if err != nil {
		return storage.NilID, err
	}
	if existing.Kind != secretstore.SecretKindOAuthTokenSet {
		return storage.NilID, fmt.Errorf("secret %q already exists with kind %q", flowData.SecretName, existing.Kind)
	}
	metadata, err := mcpOAuthSecretMetadata(existing.Metadata, flowData.Metadata, flow.EndpointURL)
	if err != nil {
		return storage.NilID, err
	}
	if _, _, err := s.store.Secrets().CreateSecretVersion(ctx, secretstore.CreateSecretVersionInput{
		OrgID:          flowData.OrgID,
		SecretID:       existing.ID,
		Material:       material,
		Actor:          identitystore.NewUserPrincipal(flowData.CreatedByUserID),
		SecretMetadata: metadata,
		MCPOAuthFlowID: flowData.FlowID,
	}); err != nil {
		return storage.NilID, err
	}
	return existing.ID, nil
}

// mcpOAuthSecretMetadata merges the flow's metadata for the saved secret:
// mcp_url and provided pairs always win, then existing pairs are kept in
// sorted key order while they fit the metadata limits, so re-authorizing
// never fails on metadata carried over from the stored secret.
func mcpOAuthSecretMetadata(
	existing json.RawMessage,
	provided resourcemeta.Metadata,
	mcpURL string,
) (resourcemeta.Metadata, error) {
	metadata := resourcemeta.Metadata{}
	maps.Copy(metadata, provided)
	metadata[secrets.KeyMCPURL] = mcpURL
	if len(existing) == 0 {
		return metadata, nil
	}
	decoded := map[string]string{}
	if err := json.Unmarshal(existing, &decoded); err != nil {
		return nil, fmt.Errorf("parse existing secret metadata: %w", err)
	}
	for _, key := range slices.Sorted(maps.Keys(decoded)) {
		if len(metadata) >= resourcemeta.MaxEntries {
			break
		}
		if _, ok := metadata[key]; ok {
			continue
		}
		if resourcemeta.ValidateEntry(key, decoded[key]) != nil {
			continue
		}
		metadata[key] = decoded[key]
	}
	return metadata, nil
}

func (s *Server) mcpOAuthClientMetadataRoute(w http.ResponseWriter, r *http.Request) {
	clientID, ok := s.mcpOAuthClientMetadataURL()
	if !ok {
		s.notFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"client_id":                  clientID,
		"client_name":                mcpOAuthClientName,
		"client_uri":                 strings.TrimRight(s.publicURL, "/"),
		"redirect_uris":              []string{s.mcpOAuthCallbackURL()},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	})
}

func (s *Server) mcpOAuthCallbackURL() string {
	return s.absolutePublicURL(mcpOAuthCallbackPath)
}

func (s *Server) mcpOAuthClientMetadataURL() (string, bool) {
	parsed, err := url.Parse(s.publicURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return "", false
	}
	return s.absolutePublicURL(mcpOAuthClientMetadataPath), true
}
